package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
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
	reasonNormal       = errors.New("normal close")
	reasonTimeout      = errors.New("timeout")
	reasonErrored      = errors.New("errored")
	reasonForceStopped = errors.New("force stopped")
)

func ReasonForceStopped() error {
	return reasonForceStopped
}

type Agent struct {
	sugar         *zap.SugaredLogger
	ctx           context.Context
	ctxCancel     context.CancelFunc
	stream        *Stream
	streamStarted bool
	// The list of IPs watching the stream.
	readIPs     []string
	redirectUrl string
}

func (a *Agent) ScheduledEndAt() time.Time {
	return a.stream.ScheduledEndAt
}
func (a *Agent) GoneOnline() bool {
	return a.stream.GoneOnline
}
func (a *Agent) UserId() string {
	return a.stream.UserId
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
func (a *Agent) RedirectUrl() string {
	return a.redirectUrl
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
			resp, err := http.Get(b.mediaServerApiUrl + "/v3/paths/get/" + strconv.FormatInt(a.stream.Id, 10))
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return errors.New("response was not 200 but " + resp.Status)
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
				// TODO:
				//a.SendGrpcStatus(pb.StreamStatus_STREAM_STARTED)
				return
			}
		}
	}()

	a.sugar.Debugw("Agent initialized", "streamId", a.stream.Id)

	// Wait for stream to come online based on the platform
	var youtubeStreamlinkInfo *YoutubeStreamlinkInfo
	var waitError error
	if a.stream.Platform == "twitch" {
		waitError = a.WaitForTwitchOnline()
	} else if a.stream.Platform == "youtube" {
		youtubeStreamlinkInfo, waitError = a.WaitForYoutubeOnline()
		if youtubeStreamlinkInfo != nil {
			a.redirectUrl = fmt.Sprintf("https://www.youtube.com/watch?v=%v", youtubeStreamlinkInfo.Metadata.Id)
		}
	} else if a.stream.Platform == "generic" {
		_, waitError = a.WaitForGenericOnline()
	} else {
		a.sugar.Panicw("Unknown platform", "streamId", a.stream.Id, "platform", a.stream.Platform)
	}
	if a.ctx.Err() != nil {
		return
	}
	if waitError != nil {
		a.Close(reasonErrored, fmt.Sprintf("Failed to wait for stream to online: %v", waitError))
		return
	}

	// Now that stream came online, start streaming

	// Save into db that the stream gone online, and set timeout for the stream
	if !a.stream.GoneOnline {
		a.sugar.Debugw("Stream gone online", "streamId", a.stream.Id)

		a.stream.ScheduledEndAt = time.Now().Add(LiveDuration)
		a.stream.GoneOnline = true
		// TODO:
		//_, err := b.db.Exec("UPDATE stream SET gone_online = 1, scheduled_end_at = ? WHERE id = ?",
		//	a.stream.ScheduledEndAt.Unix(), a.stream.Id)
		//if err != nil {
		//	a.sugar.Panicw("Failed to update stream", "streamId", a.stream.Id, "error", err)
		//}

		// TODO:
		//a.SendGrpcStatus(pb.StreamStatus_GONELIVE)
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
				a.Close(reasonErrored, string(errorStr))
			}

			a.Close(reasonNormal, "")
			return
		}
	}

}

func (a *Agent) Close(reason error, error string) {
	if a.ctx.Err() != nil {
		return
	}
	a.ctxCancel()

	//b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)

	// TODO:
	//if error != "" {
	//	_, err := b.db.Exec("UPDATE stream SET terminated_at = ?, error = ?, close_reason = ? WHERE id = ?",
	//		time.Now().Unix(), error, reason.Error(), a.stream.Id)
	//	if err != nil {
	//		a.sugar.Panicw("Failed to update stream", "streamId", a.stream.Id, "error", err)
	//	}
	//} else {
	//	_, err := b.db.Exec("UPDATE stream SET terminated_at = ?, close_reason = ? WHERE id = ?",
	//		time.Now().Unix(), reason.Error(), a.stream.Id)
	//	if err != nil {
	//		a.sugar.Panicw("Failed to update stream", "streamId", a.stream.Id, "error", err)
	//	}
	//}

	//if reason == reasonForceStopped {
	//	a.SendGrpcStatus(pb.StreamStatus_FORCE_STOPPED)
	//}
	//a.SendGrpcStatus(pb.StreamStatus_ENDED)
	//
	//for _, conn := range a.GetGrpcConns() {
	//	select {
	//	case (*conn).Error <- grpcconn.ErrClosed:
	//	default:
	//		return
	//	}
	//}

	a.sugar.Debugw("Agent closed", "streamId", a.stream.Id, "reason", reason)
}

func (a *Agent) startDummyStream(ctx context.Context, pipeWrite *io.PipeWriter) {
	go func() {
		var dummyFfmpegCmd *exec.Cmd
		dummyFfmpegCmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
			"-loglevel", "error", "-re", "-f", "lavfi", "-i",
			"color=size=1280x720:rate=2:color=black", "-stream_loop", "-1", "-i", "assets/audio/lofi-study.mp3",
			"-c:v", "libx264", "-b:v", "1500k", "-preset", "ultrafast", "-tune", "zerolatency",
			"-c:a", "aac", "-map", "0:v", "-map", "1:a", "-vf",
			`drawtext=text='Waiting for the stream to start':fontcolor=white:fontsize=56:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2-30,drawtext=text='Stream URL\: `+strings.Replace(a.stream.Url, ":", `\:`, -1)+`':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2+30,drawtext=text='%{gmtime\:%Y-%m-%d %H\\\:%M\\\:%S}':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=10:y=10`,
			"-g", "4", "-f", "mpegts", "-")
		var dummyFfmpegErrBuf bytes.Buffer
		dummyFfmpegCmd.Stdout = pipeWrite
		dummyFfmpegCmd.Stderr = &dummyFfmpegErrBuf
		dummyFfmpegCmd.Start()
		dummyFfmpegCmd.Wait()

		if a.stream.GoneOnline || ctx.Err() != nil {
			return
		}

		a.Close(reasonErrored, fmt.Sprintf("dummy stream ffmpeg failed: %v", dummyFfmpegErrBuf.String()))
		return
	}()
}

func isMediaServersFault(stderr string) bool {
	return strings.Contains(stderr, "Connection refused") || strings.Contains(stderr, "Broken pipe")
}

func (a *Agent) startFfmpegStreamer(pipe *io.PipeReader) {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)

	streamerRetryTimer := time.NewTimer(3 * time.Second)
	go func() {
		for {
			select {
			case <-a.ctx.Done():
				break
			case <-streamerRetryTimer.C:
				var streamerFfmpegCmd *exec.Cmd
				streamerFfmpegCmd = exec.CommandContext(a.ctx, "ffmpeg", "-hide_banner",
					"-loglevel", "error", "-re", "-i", "pipe:", "-c:v", "copy",
					"-c:a", "aac", "-f", "rtsp",
					fmt.Sprintf("rtsp://%s:%s@%s/%v", b.mediaServerPublishUser, b.mediaServerPublishPassword, b.mediaServerRtspHost, a.stream.Id))
				var streamerFfmpegErrBuf bytes.Buffer
				streamerFfmpegCmd.Stdin = pipe
				streamerFfmpegCmd.Stderr = &streamerFfmpegErrBuf
				streamerFfmpegCmd.Start()
				streamerFfmpegCmd.Wait()

				if a.ctx.Err() != nil {
					return
				}

				if isMediaServersFault(streamerFfmpegErrBuf.String()) {
					a.sugar.Debugw("Couldn't stream to media server. Retrying", "streamId", a.stream.Id, "error", streamerFfmpegErrBuf.String())
					streamerRetryTimer.Reset(3 * time.Second)
					continue
				}

				a.Close(reasonErrored, fmt.Sprintf("stream ffmpeg failed: %v", streamerFfmpegErrBuf.String()))
				return
			}
		}
	}()
}

func (a *Agent) checkTimeout() {
	if time.Now().After(a.stream.ScheduledEndAt) {
		a.sugar.Debugw("Agent timed out", "streamId", a.stream.Id)

		// TODO:
		//a.SendGrpcStatus(pb.StreamStatus_TIMEOUT)

		a.Close(reasonTimeout, "")

	}
}

//func (a *Agent) SendGrpcStatus(status pb.StreamStatus) {
//	for _, conn := range a.GetGrpcConns() {
//		(*conn).Stream.Send(&pb.StreamSubscribeResponse{
//			Status: status,
//		})
//	}
//}

var (
	ErrAgentClosed = errors.New("Agent is closed")
)

func (a *Agent) ResetReadIPs() error {
	if a.ctx.Err() != nil {
		return ErrAgentClosed
	}

	a.readIPs = []string{}

	// TODO:
	//a.SendGrpcStatus(pb.StreamStatus_TRY_AGAIN)

	return nil
}

//func (a *Agent) GetGrpcConns() []*grpcconn.Connection {
//	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)
//	return b.grpcConns[strconv.FormatInt(a.stream.Id, 10)]
//}
