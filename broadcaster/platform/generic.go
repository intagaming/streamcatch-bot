package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"strings"
)

var Generic name.Name = "generic"

type GenericStreamPlatform struct{}

type GenericStreamlinkInfo struct {
	Metadata struct {
		Id     string `json:"id"`
		Title  string `json:"title"`
		Author string `json:"author"`
	} `json:"metadata"`
}

func (g *GenericStreamPlatform) GetStream(ctx context.Context, s *stream.Stream) (*name.StreamData, error) {
	var streamlinkInfo GenericStreamlinkInfo

	statusCheckCmd := exec.CommandContext(ctx, "streamlink", s.Url, "-j")
	output, _ := statusCheckCmd.CombinedOutput()
	if strings.Contains(string(output), `"plugin": "`) {
		if err := json.Unmarshal(output, &streamlinkInfo); err != nil {
			return nil, err
		}
		return &name.StreamData{
			Title:    streamlinkInfo.Metadata.Title,
			StreamId: streamlinkInfo.Metadata.Id,
			Author:   streamlinkInfo.Metadata.Author,
		}, nil
	} else if strings.Contains(string(output), `No playable streams`) {
		return nil, stream.NotOnlineErr
	}
	return nil, errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
}

func (g *GenericStreamPlatform) Stream(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	return Stream(ctx,
		[]string{"streamlink", s.Url, "480p,360p", "--loglevel", "warning", "--stdout"},
		[]string{
			"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-",
		}, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}
