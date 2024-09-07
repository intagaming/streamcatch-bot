package platform

import (
	"bytes"
	"context"
	"go.uber.org/zap"
	"io"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"time"
)

var (
	localOnlineMap           = make(map[stream.Id]chan struct{})
	Local          name.Name = "local"
)

func SetLocalOnline(streamerId stream.Id) {
	localOnlineMap[streamerId] = make(chan struct{}, 1)
	localOnlineMap[streamerId] <- struct{}{}
}

type LocalStreamPlatform struct{}

func (l *LocalStreamPlatform) WaitForOnline(_ *zap.SugaredLogger, ctx context.Context, stream *stream.Stream) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return contextCancelledErr
		case <-ticker.C:
			if _, ok := localOnlineMap[stream.Id]; ok {
				select {
				case <-localOnlineMap[stream.Id]:
					return nil
				default:
					continue
				}
			}
		}
	}
}

func (l *LocalStreamPlatform) Stream(ctx context.Context, _ *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	// TODO: open ffmpeg to read from file url and print to stdout. Use this dummy thing for now.
	return Stream(ctx,
		[]string{"ffmpeg", "-hide_banner",
			"-loglevel", "error", "-re", "-f", "lavfi", "-i", "color=size=1280x720:rate=5:color=green",
			"-stream_loop", "-1", "-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
			"-c:v", "libx264", "-b:v", "1500k", "-preset", "ultrafast", "-tune", "zerolatency",
			"-c:a", "aac", "-map", "0:v", "-map", "1:a", "-vf",
			`drawtext=text='Dummy stream!':fontcolor=white:fontsize=56:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2-30,drawtext=text='%{gmtime\:%Y-%m-%d %H\\\:%M\\\:%S}':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=10:y=10`,
			"-g", "4", "-f", "mpegts", "-"},
		[]string{
			"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-",
		}, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}
