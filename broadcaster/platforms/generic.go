package platforms

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"io"
	"os/exec"
	"streamcatch-bot/broadcaster"
	"strings"
	"time"
)

type GenericStreamPlatform struct{}

//type GenericStreamlinkInfo struct {
//	Metadata struct {
//	} `json:"metadata"`
//}

func (g *GenericStreamPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *broadcaster.Stream) error {
	//var streamlinkInfo GenericStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return contextCancelledErr
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "`) {
				//if err := json.Unmarshal(output, &streamlinkInfo); err != nil {
				//	sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				//}
				// Now online
				sugar.Debugw("Detected stream live", "url", stream.Url)
				//return &streamlinkInfo, nil
				return nil
			} else if strings.Contains(string(output), `No playable streams`) {
				sugar.Debugw("Retrying getting stream", "url", stream.Url)
				continue
			} else {
				sugar.Errorw("Doesn't recognize the output from the streamlink stream checking command", "output", string(output))
				return errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}

}

func (g *GenericStreamPlatform) Stream(ctx context.Context, stream *broadcaster.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
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
