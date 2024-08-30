package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"io"
	"os/exec"
	"strings"
	"time"
)

type YoutubeStreamlinkInfo struct {
	Metadata struct {
		Id string `json:"id"`
	} `json:"metadata"`
}

func WaitForYoutubeOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *Stream) (*YoutubeStreamlinkInfo, error) {
	var youtubeStreamlinkInfo YoutubeStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, contextCancelledErr
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "youtube"`) {
				if err := json.Unmarshal(output, &youtubeStreamlinkInfo); err != nil {
					sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				}
				// Now online
				sugar.Debugw("Detected youtube stream live", "id", stream.Id, "url", stream.Url)
				return &youtubeStreamlinkInfo, nil
			} else if strings.Contains(string(output), `No playable streams`) {
				sugar.Debugw("Retrying getting stream", "id", stream.Id, "url", stream.Url)
				continue
			} else {
				sugar.Errorw("Doesn't recognize the output from the streamlink stream checking command", "output", string(output))
				return nil, errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}
}

func StreamFromYoutube(ctx context.Context, sugar *zap.SugaredLogger, stream *Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) {
	streamlinkCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "720p60,720p,480p,360p",
		"--loglevel", "warning", "--stdout")
	streamlinkCmd.Stderr = streamlinkErrBuf

	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-")

	pipe, err := streamlinkCmd.StdoutPipe()
	if err != nil {
		sugar.Panicw("Failed to create streamlink stdout pipe", "url", stream.Url, "error", err)
	}
	ffmpegCmd.Stdin = pipe

	ffmpegCmd.Stdout = pipeWrite
	ffmpegCmd.Stderr = ffmpegErrBuf

	// TODO: handle error
	streamlinkCmd.Start()
	ffmpegCmd.Start()

	ffmpegCmd.Wait()
	if streamlinkCmd.ProcessState == nil {
		streamlinkCmd.Process.Kill()
	}
}
