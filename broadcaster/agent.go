package broadcaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"streamcatch-bot/broadcaster/stream"
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
	Stream                        *stream.Stream
	ffmpegCmder                   FfmpegCmder
	dummyStreamFfmpegCmderCreator func(ctx context.Context) FfmpegCmder
}

func (a *Agent) Run() {
	b := a.ctx.Value(stream.BroadcasterCtxKey{}).(*Broadcaster)
	clock := b.config.Clock

	// Goroutine to check for stream timeout
	go func() {
		a.checkTimeout()

		ticker := clock.NewTicker(20 * time.Second)
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				a.checkTimeout()
			}
		}
	}()

	// Start the ffmpeg streamer agent
	pipeRead, pipeWrite := io.Pipe()
	a.startFfmpegStreamer(pipeRead)

	// Start streaming placeholder stream
	dummyStreamCtx, cancelDummyStreamCtx := context.WithCancel(a.ctx)
	a.startDummyStream(dummyStreamCtx, pipeWrite)
	defer cancelDummyStreamCtx()

	go func() {
		try := func() error {
			available, err := b.config.StreamAvailableChecker(a.Stream.Id)
			if err != nil {
				return err
			}
			if !available {
				return StreamNotAvailableError
			}
			return nil
		}

		retryTimer := clock.NewTimer(0)
		defer retryTimer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-retryTimer.C:
				err := try()
				if err != nil {
					a.sugar.Debugw("Stream poller: Failed to get stream info. Retrying", "streamId", a.Stream.Id, "error", err)
					retryTimer.Reset(3 * time.Second)
					continue
				}
				a.sugar.Debugw("Stream poller: Done", "streamId", a.Stream.Id)
				a.Stream.Listener.StreamStarted(a.Stream)
				return
			}
		}
	}()

	a.sugar.Debugw("Agent initialized", "streamId", a.Stream.Id)

	// Wait for stream to come online based on the platform
	platform, ok := b.config.StreamPlatforms[a.Stream.Platform]
	if !ok {
		a.Close(stream.ReasonErrored, fmt.Errorf("unknown platform: %s", a.Stream.Platform))
		return
	}
	waitError := platform.WaitForOnline(a.sugar, a.ctx, a.Stream)
	if a.ctx.Err() != nil {
		return
	}
	if waitError != nil {
		a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for stream to online: %w", waitError))
		return
	}

	// Now that stream came online, start streaming

	// Set stream gone online, and set timeout for the stream
	if a.Stream.Status != stream.StatusGoneLive {
		a.sugar.Debugw("Stream gone online", "streamId", a.Stream.Id)

		a.Stream.ScheduledEndAt = clock.Now().Add(LiveDuration)

		a.Stream.Status = stream.StatusGoneLive
		a.Stream.Listener.Status(a.Stream)
	}

	// Stop the dummy stream
	cancelDummyStreamCtx()

	// Start the real stream, overriding the dummy stream.
	// Because we are early, we will do some retry. If the stream is already
	// going for more than some amount of time, we count it as a successful
	// catch, assuming the streamer went offline, and won't retry.
	timer := clock.NewTimer(0)
	defer timer.Stop()
	attempt := 1
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
			retryStartTime := clock.Now()

			streamErr := Streamer(a.ctx, platform, b, a.Stream, pipeWrite)
			if errors.Is(streamErr, context.Canceled) {
				return
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
				return
			}

			switch {
			case streamDuration >= DurationToConsiderStreamWentOnline:
				a.Close(stream.ReasonStreamEnded, nil)
			case attempt > MaxRetries:
				a.Close(stream.ReasonErrored, FailedToStreamError)
			default:
				panic("should not be here")
			}
		}
	}
}

func (a *Agent) Close(reason stream.EndedReason, error error) {
	if a.ctx.Err() != nil {
		return
	}
	a.ctxCancel()

	a.Stream.EndedReason = &reason
	a.Stream.Status = stream.StatusEnded
	a.Stream.EndedError = error
	a.Stream.Listener.Status(a.Stream)
	a.Stream.Listener.Close(a.Stream)

	a.sugar.Debugw("Agent closed", "streamId", a.Stream.Id, "reason", reason, "error", error)
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

func (a *Agent) startFfmpegStreamer(pipe *io.PipeReader) {
	b := a.ctx.Value(stream.BroadcasterCtxKey{}).(*Broadcaster)
	clock := b.config.Clock
	streamerRetryTimer := clock.NewTimer(0)
	go func() {
		for {
			select {
			case <-a.ctx.Done():
				break
			case <-streamerRetryTimer.C:
				streamerFfmpegCmd := a.ffmpegCmder
				var streamerFfmpegErrBuf bytes.Buffer
				streamerFfmpegCmd.SetStdin(pipe)
				streamerFfmpegCmd.SetStderr(&streamerFfmpegErrBuf)
				err := streamerFfmpegCmd.Start()
				if err != nil {
					a.Close(stream.ReasonErrored, fmt.Errorf("failed to start ffmpeg cmd: %w", err))
					return
				}
				err = streamerFfmpegCmd.Wait()
				if err != nil {
					a.Close(stream.ReasonErrored, fmt.Errorf("failed to wait for ffmpeg cmd: %w", err))
					return
				}

				if a.ctx.Err() != nil {
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
	b := a.ctx.Value(stream.BroadcasterCtxKey{}).(*Broadcaster)
	clock := b.config.Clock
	if clock.Now().After(a.Stream.ScheduledEndAt) {
		if a.Stream.Status == stream.StatusGoneLive {
			a.sugar.Debugw("Agent fulfilled", "streamId", a.Stream.Id)
			a.Close(stream.ReasonFulfilled, nil)
			return
		}
		if a.Stream.Status == stream.StatusWaiting {
			a.sugar.Debugw("Agent timeout", "streamId", a.Stream.Id)
			a.Close(stream.ReasonTimeout, nil)
			return
		}
	}
}
