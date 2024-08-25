package broadcaster

import (
	"bytes"
	"encoding/json"
	"errors"
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

func (a *Agent) WaitForYoutubeOnline() (*YoutubeStreamlinkInfo, error) {
	var youtubeStreamlinkInfo YoutubeStreamlinkInfo

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return nil, nil
		case <-ticker.C:
			statusCheckCmd := exec.CommandContext(a.ctx, "streamlink", a.stream.Url, "-j")
			output, _ := statusCheckCmd.CombinedOutput()
			if strings.Contains(string(output), `"plugin": "youtube"`) {
				if err := json.Unmarshal(output, &youtubeStreamlinkInfo); err != nil {
					a.sugar.Panicw("Failed to unmarshal output", "output", string(output), "error", err)
				}
				// Now online
				a.sugar.Debugw("Detected youtube stream live", "id", a.stream.Id, "url", a.stream.Url)
				return &youtubeStreamlinkInfo, nil
			} else if strings.Contains(string(output), `No playable streams`) {
				a.sugar.Debugw("Retrying getting stream", "id", a.stream.Id, "url", a.stream.Url)
				continue
			} else {
				a.sugar.Errorw("Doesn't recognize the output from the streamlink stream checking command", "output", string(output))
				return nil, errors.New("Doesn't recognize the output from the streamlink stream checking command. Output: " + string(output))
			}
		}
	}
}

func (a *Agent) StreamFromYoutube(pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) {
	streamlinkCmd := exec.CommandContext(a.ctx, "streamlink", a.stream.Url, "720p60,720p,480p,360p",
		"--loglevel", "warning", "--stdout")
	streamlinkCmd.Stderr = streamlinkErrBuf

	ffmpegCmd := exec.CommandContext(a.ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-")

	pipe, err := streamlinkCmd.StdoutPipe()
	if err != nil {
		a.sugar.Panicw("Failed to create streamlink stdout pipe", "url", a.stream.Url, "error", err)
	}
	ffmpegCmd.Stdin = pipe

	ffmpegCmd.Stdout = pipeWrite
	ffmpegCmd.Stderr = ffmpegErrBuf

	streamlinkCmd.Start()
	ffmpegCmd.Start()

	ffmpegCmd.Wait()
	if streamlinkCmd.ProcessState == nil {
		streamlinkCmd.Process.Kill()
	}
}
