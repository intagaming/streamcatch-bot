package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net/url"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"

	"github.com/nicklaw5/helix/v2"
)

var (
	contextCancelledErr           = errors.New("context canceled")
	malformedTwitchUrl            = errors.New("couldn't find Twitch streamer name")
	channelNotFoundErr            = errors.New("channel not found")
	Twitch              name.Name = "twitch"
)

func GetTwitchStreamerNameFromUrl(twitchUrl string) (string, error) {
	u, err := url.Parse(twitchUrl)
	if err != nil {
		return "", err
	}
	paths := strings.Split(u.Path, "/")
	if len(paths) == 0 {
		return "", malformedTwitchUrl
	}
	return paths[1], nil
}

func FetchTwitchStreamerInfo(helixClient *helix.Client, url string) (*helix.User, error) {
	streamerName, err := GetTwitchStreamerNameFromUrl(url)
	if err != nil {
		return nil, err
	}
	streams, err := helixClient.GetUsers(&helix.UsersParams{
		IDs:    nil,
		Logins: []string{streamerName},
	})
	if err != nil {
		return nil, err
	}
	if len(streams.Data.Users) == 0 {
		return nil, channelNotFoundErr
	}
	return &streams.Data.Users[0], nil
}

type TwitchStreamPlatform struct {
	HelixClient     *helix.Client
	TwitchAuthToken string
}

func (t *TwitchStreamPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
	streamerName, err := GetTwitchStreamerNameFromUrl(s.Url)
	if err != nil {
		return nil, err
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, contextCancelledErr
		case <-ticker.C:
			streams, err := t.HelixClient.GetStreams(&helix.StreamsParams{
				UserLogins: []string{streamerName},
			})
			if err != nil {
				sugar.Debugw("Failed to get streams. Retrying", "streamerName", streamerName, "error", err)
				continue
			}
			if len(streams.Data.Streams) == 0 {
				sugar.Debugw("Retrying getting stream", "streamerName", streamerName,
					"rateLimit", streams.GetRateLimit(), "rateLimitRemaining", streams.GetRateLimitRemaining(),
					"rateLimitReset", streams.GetRateLimitReset())
				continue
			}
			sugar.Debugw("Detected twitch stream live", "streamerName", streamerName, "twitch stream", streams.Data.Streams[0])
			return &name.WaitForOnlineData{StreamId: streams.Data.Streams[0].ID}, nil
		}
	}
}

func (t *TwitchStreamPlatform) Stream(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	args := []string{"streamlink", s.Url, "480p,360p", "--loglevel", "warning", "--twitch-low-latency", "--hls-live-restart", "--stdout"}
	if t.TwitchAuthToken != "" {
		args = append(args, fmt.Sprintf("--twitch-api-header=Authorization=OAuth %s", t.TwitchAuthToken))
	}
	return Stream(ctx,
		args,
		[]string{
			"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-i", "pipe:", "-c:v", "copy", "-c:a", "copy", "-f", "mpegts", "-",
		}, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}
