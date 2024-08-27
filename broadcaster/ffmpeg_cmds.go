package broadcaster

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type RealFfmpegCmder struct {
	streamerFfmpegCmd *exec.Cmd
}

func (r *RealFfmpegCmder) SetStdin(pipe io.Reader) {
	r.streamerFfmpegCmd.Stdin = pipe
}

func (r *RealFfmpegCmder) SetStderr(pipe io.Writer) {
	r.streamerFfmpegCmd.Stderr = pipe
}

func (r *RealFfmpegCmder) Start() error {
	return r.streamerFfmpegCmd.Start()
}

func (r *RealFfmpegCmder) Wait() error {
	return r.streamerFfmpegCmd.Wait()
}

func NewRealFfmpegCmder(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
	streamerFfmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-loglevel", "error", "-re", "-i", "pipe:", "-c:v", "copy",
		"-c:a", "aac", "-f", "rtsp",
		fmt.Sprintf("rtsp://%s:%s@%s/%v", config.MediaServerPublishUser, config.MediaServerPublishPassword, config.MediaServerRtspHost, streamId))
	realFfmpegCmder := RealFfmpegCmder{
		streamerFfmpegCmd: streamerFfmpegCmd,
	}
	return &realFfmpegCmder
}

type RealDummyStreamFfmpegCmder struct {
	dummyFfmpegCmd *exec.Cmd
}

func (r *RealDummyStreamFfmpegCmder) SetStdout(pipe io.Writer) {
	r.dummyFfmpegCmd.Stdout = pipe
}

func (r *RealDummyStreamFfmpegCmder) SetStderr(pipe io.Writer) {
	r.dummyFfmpegCmd.Stderr = pipe
}

func (r *RealDummyStreamFfmpegCmder) Start() error {
	return r.dummyFfmpegCmd.Start()
}

func (r *RealDummyStreamFfmpegCmder) Wait() error {
	return r.dummyFfmpegCmd.Wait()
}

func NewRealDummyStreamFfmpegCmder(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
	dummyFfmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-loglevel", "error", "-re", "-f", "lavfi", "-i",
		"color=size=1280x720:rate=2:color=black", "-stream_loop", "-1", "-i", "assets/audio/lofi-study.mp3",
		"-c:v", "libx264", "-b:v", "1500k", "-preset", "ultrafast", "-tune", "zerolatency",
		"-c:a", "aac", "-map", "0:v", "-map", "1:a", "-vf",
		`drawtext=text='Waiting for the stream to start':fontcolor=white:fontsize=56:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2-30,drawtext=text='Stream URL\: `+strings.Replace(streamUrl, ":", `\:`, -1)+`':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-text_h)/2+30,drawtext=text='%{gmtime\:%Y-%m-%d %H\\\:%M\\\:%S}':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.5:boxborderw=5:x=10:y=10`,
		"-g", "4", "-f", "mpegts", "-")
	realDummyFfmpegCmder := RealDummyStreamFfmpegCmder{
		dummyFfmpegCmd: dummyFfmpegCmd,
	}
	return &realDummyFfmpegCmder
}
