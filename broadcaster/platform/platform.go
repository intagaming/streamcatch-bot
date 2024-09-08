package platform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

func Stream(ctx context.Context, streamerCmdArgs []string, ffmpegCmdArgs []string, pipeWrite *io.PipeWriter, streamerErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	localStreamerCmd := exec.CommandContext(ctx, streamerCmdArgs[0], streamerCmdArgs[1:]...)
	localStreamerCmd.Stderr = streamerErrBuf

	ffmpegCmd := exec.CommandContext(ctx, ffmpegCmdArgs[0], ffmpegCmdArgs[1:]...)

	pipe, err := localStreamerCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create streamer stdout pipe: %w", err)
	}
	ffmpegCmd.Stdin = pipe

	ffmpegCmd.Stdout = pipeWrite
	ffmpegCmd.Stderr = ffmpegErrBuf

	err = localStreamerCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start streamer cmd: %w", err)
	}
	err = ffmpegCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start ffmpeg cmd: %w", err)
	}

	err = localStreamerCmd.Wait()
	if err != nil {
		return fmt.Errorf("failed to wait for streamer cmd: %w", err)
	}
	err = ffmpegCmd.Process.Kill()
	if err != nil {
		return fmt.Errorf("failed to kill ffmpeg cmd: %w", err)
	}
	return nil
}
