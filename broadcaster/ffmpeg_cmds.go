package broadcaster

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"streamcatch-bot/broadcaster/stream"
	"strings"
)

type RealFfmpegCmder struct {
	cmd *exec.Cmd
}

func (r *RealFfmpegCmder) SetStdin(pipe *io.PipeReader) {
	r.cmd.Stdin = pipe
}

func (r *RealFfmpegCmder) SetStdout(pipe io.Writer) {
	r.cmd.Stdout = pipe
}

func (r *RealFfmpegCmder) SetStderr(pipe io.Writer) {
	r.cmd.Stderr = pipe
}

func (r *RealFfmpegCmder) Start() error {
	return r.cmd.Start()
}

func (r *RealFfmpegCmder) Wait() error {
	return r.cmd.Wait()
}

func NewRealFfmpegCmder(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
	streamerFfmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-loglevel", "error", "-re", "-i", "pipe:", "-c:v", "copy",
		"-c:a", "aac", "-f", "rtsp",
		fmt.Sprintf("rtsp://%s:%s@%s/%v", config.MediaServerPublishUser, config.MediaServerPublishPassword, config.MediaServerRtspHost, streamId))
	realFfmpegCmder := RealFfmpegCmder{
		cmd: streamerFfmpegCmd,
	}
	return &realFfmpegCmder
}

func NewRealDummyStreamFfmpegCmder(ctx context.Context, streamUrl string) FfmpegCmder {
	dummyFfmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-loglevel", "error", "-re", "-f", "lavfi", "-i",
		"color=size=1280x720:rate=2:color=black", "-stream_loop", "-1", "-i", "assets/audio/lofi-study.mp3",
		"-c:v", "libx264", "-b:v", "1500k", "-preset", "ultrafast", "-tune", "zerolatency",
		"-c:a", "aac", "-map", "0:v", "-map", "1:a", "-vf",
		`drawtext=text='Waiting for the stream to start':fontcolor=white:fontsize=56:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2-30,drawtext=text='Stream URL\: `+strings.Replace(streamUrl, ":", `\:`, -1)+`':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2+30,drawtext=text='%{gmtime\:%Y-%m-%d %H\\\:%M\\\:%S}':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=10:y=10`,
		"-g", "4", "-f", "mpegts", "-")
	realDummyFfmpegCmder := RealFfmpegCmder{
		cmd: dummyFfmpegCmd,
	}
	return &realDummyFfmpegCmder
}

type FfmpegCmder interface {
	SetStdin(pipe *io.PipeReader)
	SetStdout(pipe io.Writer)
	SetStderr(pipe io.Writer)
	Start() error
	Wait() error
}
