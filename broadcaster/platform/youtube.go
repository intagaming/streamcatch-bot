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

var YouTube name.Name = "youtube"

type YoutubeStreamlinkInfo struct {
	Metadata struct {
		Id string `json:"id"`
	} `json:"metadata"`
}

type YoutubeStreamPlatform struct{}

func (y *YoutubeStreamPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
	var youtubeStreamlinkInfo YoutubeStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, contextCancelledErr
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(ctx, "streamlink", s.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "youtube"`) {
				if err := json.Unmarshal(output, &youtubeStreamlinkInfo); err != nil {
					sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				}
				sugar.Debugw("Detected youtube stream live", "id", s.Id, "url", s.Url)
				return &name.WaitForOnlineData{StreamId: youtubeStreamlinkInfo.Metadata.Id}, nil
			} else if strings.Contains(string(output), `No playable streams`) {
				sugar.Debugw("Retrying getting stream", "id", s.Id, "url", s.Url)
				continue
			} else {
				sugar.Errorf("Doesn't recognize the output from the streamlink stream checking command: %s", string(output))
				return nil, errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}
}

func (y *YoutubeStreamPlatform) Stream(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	return Stream(ctx,
		[]string{"streamlink", s.Url, "480p,360p", "--loglevel", "warning", "--stdout"},
		[]string{
			"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-",
		}, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}
