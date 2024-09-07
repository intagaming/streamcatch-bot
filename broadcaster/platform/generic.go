package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"io"
	"os/exec"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"
)

var Generic name.Name = "generic"

type GenericStreamPlatform struct{}

type GenericStreamlinkInfo struct {
	Metadata struct {
		Id string `json:"id"`
	} `json:"metadata"`
}

func (g *GenericStreamPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *stream.Stream) (*name.WaitForOnlineData, error) {
	var streamlinkInfo GenericStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, contextCancelledErr
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(ctx, "streamlink", stream.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "`) {
				if err := json.Unmarshal(output, &streamlinkInfo); err != nil {
					sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				}
				sugar.Debugw("Detected stream live", "url", stream.Url)
				return &name.WaitForOnlineData{StreamId: streamlinkInfo.Metadata.Id}, nil
			} else if strings.Contains(string(output), `No playable streams`) {
				sugar.Debugw("Retrying getting stream", "url", stream.Url)
				continue
			} else {
				sugar.Errorw("Doesn't recognize the output from the streamlink stream checking command", "output", string(output))
				return nil, errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}

}

func (g *GenericStreamPlatform) Stream(ctx context.Context, stream *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	return Stream(ctx,
		[]string{"streamlink", stream.Url, "720p60,720p,480p,360p",
			"--loglevel", "warning", "--stdout"},
		[]string{
			"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-",
		}, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}
