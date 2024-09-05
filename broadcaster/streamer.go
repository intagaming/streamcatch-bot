package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func Streamer(ctx context.Context, broadcaster *Broadcaster, stream *Stream, pipeWrite *io.PipeWriter) error {
	var streamlinkErrBuf bytes.Buffer
	var ffmpegErrBuf bytes.Buffer

	var err error
	switch {
	case stream.Platform == "twitch":
		err = StreamFromTwitch(ctx, stream, broadcaster.config.TwitchAuthToken, pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
	case stream.Platform == "youtube":
		err = StreamFromYoutube(ctx, stream, pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
	case stream.Platform == "generic":
		err = StreamGeneric(ctx, stream, pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
	case stream.Platform == "local":
		err = StreamLocal(ctx, stream, pipeWrite, &streamlinkErrBuf, &ffmpegErrBuf)
	default:
		return errors.New("Unknown platform: " + stream.Platform)
	}
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
