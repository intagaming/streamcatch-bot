package broadcaster

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/nicklaw5/helix/v2"
)

func (a *Agent) WaitForTwitchOnline() error {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)
	u, err := url.Parse(a.stream.Url)
	if err != nil {
		return errors.New("failed to parse url")
	}
	paths := strings.Split(u.Path, "/")
	if len(paths) == 0 {
		return errors.New("couldn't find Twitch streamer name")
	}
	streamerName := paths[1]

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return nil
		case <-ticker.C:
			streams, err := b.helix.GetStreams(&helix.StreamsParams{
				UserLogins: []string{streamerName},
			})
			if err != nil {
				a.sugar.Debugw("Failed to get streams. Retrying", "streamerName", streamerName, "error", err)
			}
			if len(streams.Data.Streams) == 0 {
				a.sugar.Debugw("Retrying getting stream", "streamerName", streamerName,
					"rateLimit", streams.GetRateLimit(), "rateLimitRemaining", streams.GetRateLimitRemaining(),
					"rateLimitReset", streams.GetRateLimitReset())
				continue
			}
			// Now online
			a.sugar.Debugw("Detected twitch stream live", "streamerName", streamerName, "id", streams.Data.Streams[0].ID)
			return nil
		}
	}
}

func (a *Agent) StreamFromTwitch(pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) {
	b := a.ctx.Value(broadcasterCtxKey{}).(*Broadcaster)

	args := []string{a.stream.Url, "720p60,720p,480p,360p", "--loglevel", "warning", "--twitch-low-latency", "--hls-live-restart", "--stdout"}
	if b.twitchAuthToken != "" {
		args = append(args, fmt.Sprintf("--twitch-api-header=Authorization=OAuth %s", b.twitchAuthToken))
	}
	streamlinkCmd := exec.CommandContext(a.ctx, "streamlink", args...)
	streamlinkCmd.Stderr = streamlinkErrBuf

	ffmpegCmd := exec.CommandContext(a.ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-")

	pipe, err := streamlinkCmd.StdoutPipe()
	if err != nil {
		a.sugar.Panicw("Failed to create streamlink stdout pipe", "url", a.stream.Url, "error", err)
	}
	ffmpegCmd.Stdin = pipe

	ffmpegCmd.Stdout = pipeWrite
	ffmpegCmd.Stderr = ffmpegErrBuf

	err = streamlinkCmd.Start()
	if err != nil {
		a.sugar.Panicw("Failed to start streamlink ", "url", a.stream.Url, "error", err)
	}
	err = ffmpegCmd.Start()
	if err != nil {
		a.sugar.Panicw("Failed to start ffmpeg ", "url", a.stream.Url, "error", err)
	}

	err = ffmpegCmd.Wait()
	if err != nil {
		a.sugar.Panicw("Failed to wait ffmpeg ", "url", a.stream.Url, "error", err)
	}
	if streamlinkCmd.ProcessState == nil {
		err := streamlinkCmd.Process.Kill()
		if err != nil {
			a.sugar.Panicw("Failed to kill streamlink ", "url", a.stream.Url, "error", err)
		}
	}
}
