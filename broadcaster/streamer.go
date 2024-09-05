package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func Streamer(ctx context.Context, platform StreamPlatform, broadcaster *Broadcaster, stream *Stream, pipeWrite *io.PipeWriter) error {
	var streamlinkErrBuf bytes.Buffer
	var ffmpegErrBuf bytes.Buffer

	err := platform.Stream(ctx, stream, pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
	var ffmpegAndStreamlinkErrStr []byte

	if streamlinkErrBuf.Len() > 0 || ffmpegErrBuf.Len() > 0 {
		ffmpegAndStreamlinkErrStr, err = json.Marshal(struct {
			StreamlinkError string `json:"streamlink_error,omitempty"`
			FfmpegError     string `json:"ffmpeg_error,omitempty"`
		}{
			StreamlinkError: streamlinkErrBuf.String(),
			FfmpegError:     ffmpegErrBuf.String(),
		})
		if err != nil {
			broadcaster.sugar.Panicw("Failed to marshal error", "streamId", stream.Id, "error", err)
		}
	}

	if err != nil {
		return fmt.Errorf("errored while streaming from platform. FFMPEG and Streamlink output: %s; error: %w", ffmpegAndStreamlinkErrStr, err)
	}

	return nil
}
