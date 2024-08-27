package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
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
	ReasonNormal            = errors.New("normal close")
	ReasonTimeout           = errors.New("timeout")
	ReasonErrored           = errors.New("errored")
	ReasonForceStopped      = errors.New("force stopped")
	StreamNotAvailableError = errors.New("stream not available")
)

type Agent struct {
	sugar         *zap.SugaredLogger
	ctx           context.Context
	ctxCancel     context.CancelFunc
	stream        *Stream
	streamStarted bool
	// The list of IPs watching the stream.
	readIPs                []string
	ffmpegCmder            FfmpegCmder
	dummyStreamFfmpegCmder DummyStreamFfmpegCmder
}

func (a *Agent) ScheduledEndAt() time.Time {
	return a.stream.ScheduledEndAt
}
func (a *Agent) GoneOnline() bool {
	return a.stream.GoneOnline
}
func (a *Agent) ReadIPs() []string {
	return a.readIPs
}
func (a *Agent) AddIp(ip string) {
	a.readIPs = append(a.readIPs, ip)
}
func (a *Agent) StreamStarted() bool {
	return a.streamStarted
}
func (a *Agent) StreamUrl() string {
	return a.stream.Url
}

func (a *Agent) Run() {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)

	// Goroutine to check for stream timeout
	go func() {
		a.checkTimeout()

		ticker := time.NewTicker(20 * time.Second)
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

		retryTimer := time.NewTimer(2 * time.Second)
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-retryTimer.C:
				err := try()
				if err != nil {
					a.sugar.Debugw("Stream poller: Failed to get stream info. Retrying", "streamId", a.stream.Id, "error", err)
					retryTimer.Reset(3 * time.Second)
					continue
				}
				a.sugar.Debugw("Stream poller: Done", "streamId", a.stream.Id)
				a.streamStarted = true
				a.stream.Listener.Status(a.stream, StreamStarted)
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
		a.Close(ReasonErrored, fmt.Sprintf("Failed to wait for stream to online: %v", waitError))
		return
	}

	// Now that stream came online, start streaming

	// Set stream gone online, and set timeout for the stream
	if !a.stream.GoneOnline {
		a.sugar.Debugw("Stream gone online", "streamId", a.stream.Id)

		a.stream.ScheduledEndAt = time.Now().Add(LiveDuration)
		a.stream.GoneOnline = true

		a.stream.Listener.Status(a.stream, GoneLive)
	}

	// Stop the dummy stream
	cancelDummyStreamCtx()

	// Start the real stream, overriding the dummy stream.
	// Because we are early, we will do some retry. If the stream is already
	// going for more than some amount of time, we count it as a successful
	// catch, assuming the streamer went offline, and won't retry.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	attempt := 1
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			retryStartTime := time.Now()

			var streamlinkErrBuf bytes.Buffer
			var ffmpegErrBuf bytes.Buffer

			// TODO: abstract away
			if a.stream.Platform == "twitch" {
				a.StreamFromTwitch(pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
			} else if a.stream.Platform == "youtube" {
				a.StreamFromYoutube(pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
			} else if a.stream.Platform == "generic" {
				a.StreamGeneric(pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
			} else {
				a.sugar.Panicw("Unknown platform", "streamId", a.stream.Id, "platform", a.stream.Platform)
			}
			if a.ctx.Err() != nil {
				return
			}

			retryEndTime := time.Now()

			if attempt < MaxRetries && retryEndTime.Sub(retryStartTime) < DurationToConsiderCatchedOk {
				a.sugar.Debugw("Retrying getting stream", "streamId", a.stream.Id, "url", a.stream.Url)
				attempt++
				continue
			}

			if streamlinkErrBuf.Len() > 0 || ffmpegErrBuf.Len() > 0 {
				errorStr, err := json.Marshal(struct {
					StreamlinkError string `json:"streamlink_error,omitempty"`
					FfmpegError     string `json:"ffmpeg_error,omitempty"`
				}{
					StreamlinkError: streamlinkErrBuf.String(),
					FfmpegError:     ffmpegErrBuf.String(),
				})
				if err != nil {
					a.sugar.Panicw("Failed to marshal error", "streamId", a.stream.Id, "error", err)
				}
				a.sugar.Debugw("Stream terminated", "streamId", a.stream.Id, "error", string(errorStr))
				a.Close(ReasonErrored, string(errorStr))
			}

			a.Close(ReasonNormal, "")
			return
		}
	}

}

func (a *Agent) Close(reason error, error string) {
	if a.ctx.Err() != nil {
		return
	}
	a.ctxCancel()

	if errors.Is(reason, ReasonForceStopped) {
		a.stream.Listener.Status(a.stream, ForceStopped)
	}
	a.stream.Listener.Status(a.stream, Ended)
	a.stream.Listener.Close(a.stream, reason)

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
		dummyFfmpegCmd := a.dummyStreamFfmpegCmder
		var dummyFfmpegCombinedBuf bytes.Buffer
		w := io.MultiWriter(pipeWrite, &dummyFfmpegCombinedBuf)
		dummyFfmpegCmd.SetStdout(w)
		dummyFfmpegCmd.SetStderr(&dummyFfmpegCombinedBuf)
		err := dummyFfmpegCmd.Start()
		if err != nil {
			a.Close(ReasonErrored, fmt.Sprintf("failed to start dummy stream ffmpeg: %v; ffmpeg output: %s", err, dummyFfmpegCombinedBuf))
			return
		}
		err = dummyFfmpegCmd.Wait()
		if err != nil {
			a.Close(ReasonErrored, fmt.Sprintf("failed to wait for dummy stream ffmpeg cmd: %v; ffmpeg output: %s", err, dummyFfmpegCombinedBuf))
			return
		}

		if a.stream.GoneOnline || ctx.Err() != nil {
			return
		}

		a.Close(ReasonErrored, fmt.Sprintf("dummy stream ffmpeg failed; ffmpeg output: %s", dummyFfmpegCombinedBuf))
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
	streamerRetryTimer := time.NewTimer(3 * time.Second)
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
					a.Close(ReasonErrored, fmt.Sprintf("failed to start ffmpeg cmd: %v", err))
					return
				}
				err = streamerFfmpegCmd.Wait()
				if err != nil {
					a.Close(ReasonErrored, fmt.Sprintf("failed to wait for ffmpeg cmd: %v", err))
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

				a.Close(ReasonErrored, fmt.Sprintf("stream ffmpeg failed: %v", streamerFfmpegErrBuf.String()))
				return
			}
		}
	}()
}

func (a *Agent) checkTimeout() {
	if time.Now().After(a.stream.ScheduledEndAt) {
		a.sugar.Debugw("Agent timed out", "streamId", a.stream.Id)

		a.stream.Listener.Status(a.stream, Timeout)

		a.Close(ReasonTimeout, "")

	}
}
