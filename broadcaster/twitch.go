package broadcaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/nicklaw5/helix/v2"
)

var contextCancelledErr = errors.New("context canceled")

func WaitForTwitchOnline(sugar *zap.SugaredLogger, ctx context.Context, helixClient *helix.Client, stream *Stream) error {
	u, err := url.Parse(stream.Url)
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
		case <-ctx.Done():
			return contextCancelledErr
		case <-ticker.C:
			streams, err := helixClient.GetStreams(&helix.StreamsParams{
				UserLogins: []string{streamerName},
			})
			if err != nil {
				sugar.Debugw("Failed to get streams. Retrying", "streamerName", streamerName, "error", err)
			}
			if len(streams.Data.Streams) == 0 {
				sugar.Debugw("Retrying getting stream", "streamerName", streamerName,
					"rateLimit", streams.GetRateLimit(), "rateLimitRemaining", streams.GetRateLimitRemaining(),
					"rateLimitReset", streams.GetRateLimitReset())
				continue
			}
			// Now online
			sugar.Debugw("Detected twitch stream live", "streamerName", streamerName, "id", streams.Data.Streams[0].ID)
			return nil
		}
	}
}

func StreamFromTwitch(ctx context.Context, stream *Stream, twitchAuthToken string, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	args := []string{stream.Url, "720p60,720p,480p,360p", "--loglevel", "warning", "--twitch-low-latency", "--hls-live-restart", "--stdout"}
	if twitchAuthToken != "" {
		args = append(args, fmt.Sprintf("--twitch-api-header=Authorization=OAuth %s", twitchAuthToken))
	}
	streamlinkCmd := exec.CommandContext(ctx, "streamlink", args...)
	streamlinkCmd.Stderr = streamlinkErrBuf

	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-")

	pipe, err := streamlinkCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create streamlink stdout pipe: %w", err)
	}
	ffmpegCmd.Stdin = pipe

	ffmpegCmd.Stdout = pipeWrite
	ffmpegCmd.Stderr = ffmpegErrBuf

	err = streamlinkCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start streamlink: %w", err)
	}
	err = ffmpegCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	err = ffmpegCmd.Wait()
	if err != nil {
		return fmt.Errorf("failed to wait ffmpeg: %w", err)
	}
	if streamlinkCmd.ProcessState == nil {
		err := streamlinkCmd.Process.Kill()
		if err != nil {
			return fmt.Errorf("failed to kill streamlink: %w", err)
		}
	}
	return nil
}
