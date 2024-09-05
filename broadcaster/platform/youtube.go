package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"io"
	"os/exec"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"
)

var YouTube name.Name = "youtube"

//type YoutubeStreamlinkInfo struct {
//	Metadata struct {
//		Id string `json:"id"`
//	} `json:"metadata"`
//}

type YoutubeStreamPlatform struct{}

func (y *YoutubeStreamPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *stream.Stream) error {
	//var youtubeStreamlinkInfo YoutubeStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return contextCancelledErr
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "youtube"`) {
				//if err := json.Unmarshal(output, &youtubeStreamlinkInfo); err != nil {
				//	sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				//}
				// Now online
				sugar.Debugw("Detected youtube stream live", "id", stream.Id, "url", stream.Url)
				//return &youtubeStreamlinkInfo, nil
				return nil
			} else if strings.Contains(string(output), `No playable streams`) {
				sugar.Debugw("Retrying getting stream", "id", stream.Id, "url", stream.Url)
				continue
			} else {
				sugar.Errorw("Doesn't recognize the output from the streamlink stream checking command", "output", string(output))
				return errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}
}

func (y *YoutubeStreamPlatform) Stream(ctx context.Context, stream *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	streamlinkCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "720p60,720p,480p,360p",
		"--loglevel", "warning", "--stdout")
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
		return fmt.Errorf("failed to start streamlink cmd: %w", err)
	}
	err = ffmpegCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start ffmpeg cmd: %w", err)
	}

	err = ffmpegCmd.Wait()
	if err != nil {
		return fmt.Errorf("failed to wait for ffmpeg cmd: %w", err)
	}
	if streamlinkCmd.ProcessState == nil {
		err := streamlinkCmd.Process.Kill()
		if err != nil {
			return fmt.Errorf("failed to kill streamlink cmd: %w", err)
		}
	}
	return nil
}
