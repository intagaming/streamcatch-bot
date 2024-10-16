package broadcaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/scredis"
	"time"

	"go.uber.org/zap"
)

const (
	MaxRetries                         = 3
	LiveDuration                       = 10 * time.Minute
	DurationToConsiderStreamWentOnline = 30 * time.Second
)

var (
	StreamNotAvailableError = errors.New("stream not available")
	FailedToStreamError     = errors.New("could not download stream from platform")
)

type Agent struct {
	sugar                         *zap.SugaredLogger
	ctx                           context.Context
	ctxCancel                     context.CancelFunc
	iterationCtx                  context.Context
	iterationCtxCancel            context.CancelFunc
	Stream                        *stream.Stream
	ffmpegCmder                   func(ctx context.Context) FfmpegCmder
	dummyStreamFfmpegCmderCreator func(ctx context.Context) FfmpegCmder
}

func (a *Agent) Broadcaster() *Broadcaster {
	return a.ctx.Value(stream.BroadcasterCtxKey{}).(*Broadcaster)
}

func (a *Agent) Run() {
	a.sugar.Debugw("Agent running", "stream", a.Stream)

	// If stream timed out, don't run further.
	a.checkTimeout()
	if a.ctx.Err() != nil {
		return
	}

	// TODO: properly resume stream. For now, make it work like a new stream.
	a.Stream.Live = false
	a.Stream.LastStatus = stream.StatusWaiting
	a.Stream.Status = stream.StatusWaiting
	// TODO: store to db

	clock := a.Broadcaster().Config.Clock
	go func() {
		t := clock.TickerFunc(a.ctx, scredis.MutexDuration/5, func() error {
			_, err := a.Stream.Mutex.Extend()
			return err
		}, "StreamMutexExtender")
		err := t.Wait()
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			a.sugar.Errorf("Error extending stream mutex: %v", err)
		}
	}()

	// This loop will be running only once if the stream is not permanent.
	for {
		a.HandleOneStreamInstance()
		if !a.Stream.Permanent || a.ctx.Err() != nil {
			break
		}
		a.sugar.Debugw("Waiting 5 seconds before handle next instance")
		continueCtx, cancel := context.WithCancel(a.ctx)
		clock.AfterFunc(5*time.Second, func() {
			cancel()
		})
		select {
		case <-continueCtx.Done():
			continue
		case <-a.ctx.Done():
			break
		}
	}
}

func (a *Agent) HandleOneStreamInstance() {
	a.sugar.Debugw("HandleOneStreamInstance being called", "streamId", a.Stream.Id)

	b := a.Broadcaster()
	clock := b.Config.Clock

	iterationCtx, cancelIterationCtx := context.WithCancel(a.ctx)
	defer cancelIterationCtx()

	a.iterationCtx = iterationCtx
	a.iterationCtxCancel = cancelIterationCtx
	defer func() {
		a.iterationCtx = nil
		a.iterationCtxCancel = nil
	}()

	go a.TimeoutChecker(iterationCtx)
	go a.StreamPoller(iterationCtx)

	pipeRead, pipeWrite := io.Pipe()
	dummyStreamCtx, cancelDummyStreamCtx := context.WithCancel(iterationCtx)
	defer cancelDummyStreamCtx()
	if !a.Stream.Permanent {
		a.startFfmpegStreamer(iterationCtx, pipeRead)
		a.startDummyStream(dummyStreamCtx, pipeWrite)
	}

	platform, ok := b.Config.StreamPlatforms[a.Stream.Platform]
	if !ok {
		a.Close(stream.ReasonErrored, fmt.Errorf("unknown platform: %s", a.Stream.Platform))
		return
	}

	// If stream is live, it is a permanent stream that's being handled. Must wait
	// until stream comes offline to handle next stream instance.
	if a.Stream.Permanent && a.Stream.Live {
		a.sugar.Debugw("Waiting for stream to come offline", "streamId", a.Stream.Id)
		err := a.WaitUntilOffline(iterationCtx, a.Stream)
		if err != nil {
			a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for stream to come offline: %w", err))
			return
		}
		a.Stream.Live = false
		// TODO: store db
	}

	// Wait for stream to come online
	a.sugar.Debugw("Waiting for stream to come online", "streamId", a.Stream.Id)
	data, waitError := a.WaitUntilOnline(a.sugar, iterationCtx, a.Stream)
	if waitError != nil {
		a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for stream to online: %w", waitError))
		return
	}
	a.Stream.Live = true
	a.Stream.LastLiveAt = clock.Now()
	a.Stream.Title = data.Title
	a.Stream.Author = data.Author

	a.sugar.Infow("Stream gone live", "streamId", a.Stream.Id, "Platform StreamId", data.StreamId)

	// Now that stream came online, start streaming
	if a.Stream.Permanent {
		a.startFfmpegStreamer(iterationCtx, pipeRead)
	}

	// Set stream gone online, and set timeout for the stream
	a.Stream.ScheduledEndAt = clock.Now().Add(LiveDuration)
	a.Stream.ChangeStatus(stream.StatusGoneLive)
	a.Stream.Listener.Status(a.Stream)

	// Stop the dummy stream, if any
	cancelDummyStreamCtx()

	// Start the real stream, overriding the dummy stream (if any).
	// Because we are early, we will do some retry. If the stream is already
	// going for more than some amount of time, we count it as a successful
	// catch, assuming the streamer went offline, and won't retry.
	timer := clock.NewTimer(0)
	defer timer.Stop()
	attempt := 1
out:
	for {
		select {
		case <-iterationCtx.Done():
			break out
		case <-timer.C:
			retryStartTime := clock.Now()

			streamErr := Streamer(iterationCtx, platform, b, a.Stream, pipeWrite)
			if errors.Is(streamErr, context.Canceled) {
				break out
			}

			retryEndTime := clock.Now()
			streamDuration := retryEndTime.Sub(retryStartTime)

			if attempt <= MaxRetries && streamDuration < DurationToConsiderStreamWentOnline {
				a.sugar.Debugw("Retrying getting stream", "streamId", a.Stream.Id, "url", a.Stream.Url, "streamErr", streamErr)
				attempt++
				timer.Reset(10 * time.Second)
				continue
			}

			if streamErr != nil {
				a.sugar.Debugw("Stream error", "streamId", a.Stream.Id, "error", streamErr)
				a.Close(stream.ReasonErrored, streamErr)
				break out
			}

			switch {
			case streamDuration >= DurationToConsiderStreamWentOnline:
				// If stream is i.e. fulfilled, this close will be cancelled
				a.Close(stream.ReasonStreamEnded, nil)
			case attempt > MaxRetries:
				a.Close(stream.ReasonErrored, FailedToStreamError)
			default:
				panic("should not be here")
			}
			break out
		}
	}
}

func (a *Agent) Close(reason stream.EndedReason, err error) bool {
	if errors.Is(err, context.Canceled) || (a.ctx.Err() != nil) || (a.iterationCtx != nil && a.iterationCtx.Err() != nil) {
		return false
	}

	if reason == stream.ReasonErrored {
		a.sugar.Errorf("Stream %s, URL %s closed with error: %v", a.Stream.Id, a.Stream.Url, err)
	}

	if !a.Stream.Permanent || reason == stream.ReasonForceStopped {
		a.ctxCancel()
		a.sugar.Infow("Agent closed", "streamId", a.Stream.Id, "reason", reason, "error", err)
	} else {
		a.iterationCtxCancel()
		a.sugar.Infow("Agent iteration closed", "streamId", a.Stream.Id, "reason", reason, "error", err, "iter err", a.iterationCtx.Err())
	}

	if !a.Stream.Permanent || reason == stream.ReasonForceStopped {
		a.Stream.ChangeStatus(stream.StatusEnded)
	} else {
		a.Stream.ChangeStatus(stream.StatusWaiting)
		a.Stream.ScheduledEndAt = time.Time{}
	}
	a.Stream.EndedReason = &reason
	a.Stream.EndedError = err

	a.Stream.Listener.Status(a.Stream)
	a.Stream.Listener.Close(a.Stream)

	switch reason {
	case stream.ReasonFulfilled, stream.ReasonStopOneInstance,
		stream.ReasonStreamEnded, stream.ReasonForceStopped:
		go a.Broadcaster().combineRecordings(*a.Stream, a.Stream.LastLiveAt, time.Now())
	default:
	}
	return true
}

func (a *Agent) StreamPoller(ctx context.Context) {
	b := a.Broadcaster()
	clock := b.Config.Clock
	try := func() error {
		available, err := b.Config.StreamAvailableChecker(a.Stream.Id)
		if err != nil {
			return err
		}
		if !available {
			return StreamNotAvailableError
		}
		return nil
	}

	tickerCtx, tickerCancel := context.WithCancel(ctx)
	t := clock.TickerFunc(tickerCtx, 3*time.Second, func() error {
		if a.Stream.Permanent && a.Stream.Status != stream.StatusGoneLive {
			return nil
		}
		err := try()
		if err != nil {
			if !a.Stream.Permanent || a.Stream.Status == stream.StatusGoneLive {
				a.sugar.Debugw("StreamPoller: Failed to get stream info. Retrying", "streamId", a.Stream.Id, "error", err)
			}
			return nil
		}
		a.sugar.Debugw("StreamPoller: Done", "streamId", a.Stream.Id)
		a.Stream.SCStreamStarted = true
		a.Stream.Listener.StreamStarted(a.Stream)
		tickerCancel()
		return nil
	}, "StreamPoller")
	err := t.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		a.sugar.Warnw("StreamPoller: Should have not error", "streamId", a.Stream.Id, "err", err)
	}
}

func (a *Agent) TimeoutChecker(ctx context.Context) {
	b := a.Broadcaster()
	clock := b.Config.Clock
	a.checkTimeout()

	ticker := clock.NewTicker(20 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkTimeout()
		}
	}
}

func (a *Agent) startDummyStream(ctx context.Context, pipeWrite *io.PipeWriter) {
	go func() {
		dummyFfmpegCmd := a.dummyStreamFfmpegCmderCreator(ctx)
		var dummyFfmpegCombinedBuf bytes.Buffer
		w := io.MultiWriter(pipeWrite, &dummyFfmpegCombinedBuf)
		dummyFfmpegCmd.SetStdout(w)
		dummyFfmpegCmd.SetStderr(&dummyFfmpegCombinedBuf)
		err := dummyFfmpegCmd.Start()
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			a.Close(stream.ReasonErrored, fmt.Errorf("failed to start dummy stream ffmpeg: %w; ffmpeg output: %s", err, dummyFfmpegCombinedBuf.String()))
			return
		}
		err = dummyFfmpegCmd.Wait()
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for dummy stream ffmpeg cmd: %w; ffmpeg output: %s", err, dummyFfmpegCombinedBuf.String()))
			return
		}

		if a.Stream.Status == stream.StatusGoneLive || ctx.Err() != nil {
			return
		}

		a.Close(stream.ReasonErrored, fmt.Errorf("dummy stream ffmpeg failed; ffmpeg output: %s", dummyFfmpegCombinedBuf.String()))
		return
	}()
}

func (a *Agent) startFfmpegStreamer(ctx context.Context, pipe *io.PipeReader) {
	b := a.Broadcaster()
	clock := b.Config.Clock
	streamerRetryTimer := clock.NewTimer(0)
	go func() {
		for {
			select {
			case <-ctx.Done():
				break
			case <-streamerRetryTimer.C:
				streamerFfmpegCmd := a.ffmpegCmder(ctx)
				var streamerFfmpegErrBuf bytes.Buffer
				streamerFfmpegCmd.SetStdin(pipe)
				streamerFfmpegCmd.SetStderr(&streamerFfmpegErrBuf)
				err := streamerFfmpegCmd.Start()
				if err != nil {
					if errors.Is(ctx.Err(), context.Canceled) {
						return
					}
					a.Close(stream.ReasonErrored, fmt.Errorf("failed to start ffmpeg cmd: %w", err))
					return
				}
				err = streamerFfmpegCmd.Wait()
				if err != nil {
					if errors.Is(ctx.Err(), context.Canceled) {
						return
					}
					a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for ffmpeg cmd: %w", err))
					return
				}

				if isMediaServersFault(streamerFfmpegErrBuf.String()) {
					a.sugar.Debugw("Couldn't stream to media server. Retrying", "streamId", a.Stream.Id, "error", streamerFfmpegErrBuf.String())
					streamerRetryTimer.Reset(3 * time.Second)
					continue
				}

				a.Close(stream.ReasonErrored, fmt.Errorf("stream ffmpeg failed: %v", streamerFfmpegErrBuf.String()))
				return
			}
		}
	}()
}

func (a *Agent) checkTimeout() {
	b := a.Broadcaster()
	clock := b.Config.Clock
	if clock.Now().After(a.Stream.ScheduledEndAt) {
		if a.Stream.Status == stream.StatusGoneLive {
			a.sugar.Debugw("Agent fulfilled", "streamId", a.Stream.Id)
			a.Close(stream.ReasonFulfilled, nil)
			return
		}
		if !a.Stream.Permanent && a.Stream.Status == stream.StatusWaiting {
			a.sugar.Debugw("Agent timeout", "streamId", a.Stream.Id)
			a.Close(stream.ReasonTimeout, nil)
			return
		}
	}
}

func (a *Agent) WaitUntilOffline(ctx context.Context, s *stream.Stream) error {
	platform, ok := a.Broadcaster().Config.StreamPlatforms[a.Stream.Platform]
	if !ok {
		return fmt.Errorf("unknown platform: %s", a.Stream.Platform)
	}
	okErr := errors.New("ok")
	t := a.Broadcaster().Config.Clock.TickerFunc(ctx, 3*time.Second, func() error {
		_, err := platform.GetStream(ctx, s)
		if err != nil {
			if errors.Is(err, stream.NotOnlineErr) {
				return okErr
			}
			return err
		}
		return nil
	}, "WaitUntilOffline")
	err := t.Wait()
	if errors.Is(err, okErr) {
		return nil
	}
	return err
}

func (a *Agent) WaitUntilOnline(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.StreamData, error) {
	platform, ok := a.Broadcaster().Config.StreamPlatforms[a.Stream.Platform]
	if !ok {
		return nil, fmt.Errorf("unknown platform: %s", a.Stream.Platform)
	}
	foundErr := errors.New("found")
	var data *name.StreamData
	t := a.Broadcaster().Config.Clock.TickerFunc(ctx, 3*time.Second, func() error {
		var err error
		data, err = platform.GetStream(ctx, s)
		if err != nil {
			if errors.Is(err, stream.NotOnlineErr) {
				sugar.Debugw("Stream not online. Retrying getting stream", "streamId", s.Id)
			} else {
				sugar.Debugw("Failed to get streams. Retrying", "streamId", s.Id, "error", err)
			}
			return nil
		}
		sugar.Debugw("Detected stream live", "streamId", s.Id, "data", data)
		return foundErr
	}, "WaitUntilOnline")
	err := t.Wait()
	if errors.Is(err, foundErr) {
		return data, nil
	}
	return nil, err
}
