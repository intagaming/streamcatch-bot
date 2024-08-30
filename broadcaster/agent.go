package broadcaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	MaxRetries                  = 3
	LiveDuration                = 10 * time.Minute
	DurationToConsiderCatchedOk = 30 * time.Second
)

var (
	StreamNotAvailableError = errors.New("stream not available")
)

type Agent struct {
	sugar     *zap.SugaredLogger
	ctx       context.Context
	ctxCancel context.CancelFunc
	stream    *Stream
	// The list of IPs watching the stream.
	//readIPs                       []string
	ffmpegCmder                   FfmpegCmder
	dummyStreamFfmpegCmderCreator func(ctx context.Context) DummyStreamFfmpegCmder
}

// TODO: remove getters
func (a *Agent) ScheduledEndAt() time.Time {
	return a.stream.ScheduledEndAt
}

//func (a *Agent) GoneOnline() bool {
//	return a.stream.GoneOnline
//}

//	func (a *Agent) ReadIPs() []string {
//		return a.readIPs
//	}
//
//	func (a *Agent) AddIp(ip string) {
//		a.readIPs = append(a.readIPs, ip)
//	}
func (a *Agent) StreamUrl() string {
	return a.stream.Url
}

func (a *Agent) Run() {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)
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
			available, err := b.config.StreamAvailableChecker(a.stream.Id)
			if err != nil {
				return err
			}
			if !available {
				return StreamNotAvailableError
			}
			return nil
		}

		retryTimer := clock.NewTicker(2 * time.Second)
		defer retryTimer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-retryTimer.C:
				err := try()
				if err != nil {
					a.sugar.Debugw("Stream poller: Failed to get stream info. Retrying", "streamId", a.stream.Id, "error", err)
					//retryTimer.Reset(3 * time.Second)
					continue
				}
				a.sugar.Debugw("Stream poller: Done", "streamId", a.stream.Id)
				// TODO: is this proper notify?
				a.stream.Listener.StreamStarted(a.stream)
				return
			}
		}
	}()

	a.sugar.Debugw("Agent initialized", "streamId", a.stream.Id)

	// Wait for stream to come online based on the platform
	waitError := b.config.StreamWaiter(a)
	if a.ctx.Err() != nil {
		return
	}
	if waitError != nil {
		a.Close(Errored, fmt.Errorf("failed to wait for stream to online: %w", waitError))
		return
	}

	// Now that stream came online, start streaming

	// Set stream gone online, and set timeout for the stream
	if a.stream.Status != GoneLive {
		a.sugar.Debugw("Stream gone online", "streamId", a.stream.Id)

		a.stream.ScheduledEndAt = clock.Now().Add(LiveDuration)

		a.stream.Status = GoneLive
		a.stream.Listener.Status(a.stream)
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

			streamErr := b.config.Streamer(a.ctx, a.stream, pipeWrite)

			retryEndTime := clock.Now()

			if attempt < MaxRetries && retryEndTime.Sub(retryStartTime) < DurationToConsiderCatchedOk {
				a.sugar.Debugw("Retrying getting stream", "streamId", a.stream.Id, "url", a.stream.Url)
				attempt++
				timer.Reset(10 * time.Second)
				continue
			}

			if streamErr != nil {
				a.sugar.Debugw("Stream terminated", "streamId", a.stream.Id, "error", streamErr)
				a.Close(Errored, streamErr)
				return
			}

			a.Close(Fulfilled, nil)
			return
		}
	}

}

func (a *Agent) Close(reason EndedReason, error error) {
	if a.ctx.Err() != nil {
		return
	}
	a.ctxCancel()

	//if reason == ForceStopped {
	//	a.stream.Listener.Status(a.stream, ForceStopped)
	//}
	a.stream.EndedReason = &reason
	a.stream.Status = Ended
	a.stream.Listener.Status(a.stream)
	a.stream.Listener.Close(a.stream)

	a.sugar.Debugw("Agent closed", "streamId", a.stream.Id, "reason", reason, "error", error)
}

type DummyStreamFfmpegCmder interface {
	SetStdout(pipe io.Writer)
	SetStderr(pipe io.Writer)
	Start() error
	Wait() error
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
			a.Close(Errored, fmt.Errorf("failed to start dummy stream ffmpeg: %w; ffmpeg output: %s", err, dummyFfmpegCombinedBuf.String()))
			return
		}
		err = dummyFfmpegCmd.Wait()
		if err != nil {
			a.Close(Errored, fmt.Errorf("failed to wait for dummy stream ffmpeg cmd: %w; ffmpeg output: %s", err, dummyFfmpegCombinedBuf.String()))
			return
		}

		if a.stream.Status == GoneLive || ctx.Err() != nil {
			return
		}

		a.Close(Errored, fmt.Errorf("dummy stream ffmpeg failed; ffmpeg output: %s", dummyFfmpegCombinedBuf.String()))
		return
	}()
}

func isMediaServersFault(stderr string) bool {
	return strings.Contains(stderr, "Connection refused") || strings.Contains(stderr, "Broken pipe")
}

type FfmpegCmder interface {
	SetStdin(pipe io.Reader)
	SetStderr(pipe io.Writer)
	Start() error
	Wait() error
}

func (a *Agent) startFfmpegStreamer(pipe *io.PipeReader) {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)
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
					a.Close(Errored, fmt.Errorf("failed to start ffmpeg cmd: %w", err))
					return
				}
				err = streamerFfmpegCmd.Wait()
				if err != nil {
					a.Close(Errored, fmt.Errorf("failed to wait for ffmpeg cmd: %w", err))
					return
				}

				if a.ctx.Err() != nil {
					return
				}

				if isMediaServersFault(streamerFfmpegErrBuf.String()) {
					a.sugar.Debugw("Couldn't stream to media server. Retrying", "streamId", a.stream.Id, "error", streamerFfmpegErrBuf.String())
					streamerRetryTimer.Reset(3 * time.Second)
					continue
				}

				a.Close(Errored, fmt.Errorf("stream ffmpeg failed: %v", streamerFfmpegErrBuf.String()))
				return
			}
		}
	}()
}

func (a *Agent) checkTimeout() {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)
	clock := b.config.Clock
	if clock.Now().After(a.stream.ScheduledEndAt) {
		a.sugar.Debugw("Agent timed out", "streamId", a.stream.Id)
		a.Close(Timeout, nil)
	}
}
