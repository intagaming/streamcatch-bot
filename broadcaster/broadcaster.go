package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
	"os/exec"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"
)

const (
	ScheduledEndDuration = 30 * time.Minute
)

var (
	errInvalidUrl = errors.New("invalid stream url")
)

type Config struct {
	TwitchClientId                string
	TwitchClientSecret            string
	TwitchAuthToken               string
	MediaServerRtspHost           string
	MediaServerPublishUser        string
	MediaServerPublishPassword    string
	MediaServerApiUrl             string
	FfmpegCmderCreator            func(ctx context.Context, config *Config, streamId int64) FfmpegCmder
	DummyStreamFfmpegCmderCreator func(ctx context.Context, streamUrl string) FfmpegCmder
	StreamAvailableChecker        func(streamId int64) (bool, error)
	Helix                         *helix.Client
	StreamPlatforms               map[name.Name]stream.Platform
	Clock                         quartz.Clock
	StreamerInfoFetcher           func(ctx context.Context, stream *stream.Stream) (*stream.Info, error)
}

type Broadcaster struct {
	sugar  *zap.SugaredLogger
	agents map[int64]*Agent
	helix  *helix.Client
	config *Config
}

func (b *Broadcaster) Agents() map[int64]*Agent {
	return b.agents
}

func (b *Broadcaster) MediaServerPublishUser() string {
	return b.config.MediaServerPublishUser
}

func (b *Broadcaster) MediaServerPublishPassword() string {
	return b.config.MediaServerPublishPassword
}

var idCount int64 = 0

func New(sugar *zap.SugaredLogger, cfg *Config) *Broadcaster {
	b := Broadcaster{
		sugar:  sugar,
		agents: make(map[int64]*Agent),
		helix:  cfg.Helix,
		config: cfg,
	}

	return &b
}

func (b *Broadcaster) MakeLocalStream(ctx context.Context, url string, listener stream.StatusListener) (*stream.Stream, error) {
	idCount += 1
	stream := stream.Stream{
		Id:             idCount,
		Url:            url,
		Platform:       platform.Local,
		CreatedAt:      time.Now(),
		ScheduledEndAt: time.Now().Add(ScheduledEndDuration),
		Listener:       listener,
	}

	info, err := b.config.StreamerInfoFetcher(ctx, &stream)
	if err != nil {
		return nil, err
	}
	stream.ThumbnailUrl = info.ThumbnailUrl

	return &stream, nil
}

func (b *Broadcaster) MakeStream(ctx context.Context, url string, listener stream.StatusListener) (*stream.Stream, error) {
	checkCmd := exec.CommandContext(ctx, "streamlink", "--can-handle-url", url)
	err := checkCmd.Run()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			if exitError.ExitCode() == 1 {
				return nil, errInvalidUrl
			}
			return nil, exitError
		}
		return nil, err
	}

	var platformName name.Name
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		platformName = platform.YouTube
	} else if strings.Contains(url, "twitch.tv") {
		platformName = platform.Twitch
	} else {
		platformName = platform.Generic
	}

	idCount += 1
	s := stream.Stream{
		Id:             idCount,
		Url:            url,
		Platform:       platformName,
		CreatedAt:      time.Now(),
		ScheduledEndAt: time.Now().Add(ScheduledEndDuration),
		Listener:       listener,
	}

	info, err := b.config.StreamerInfoFetcher(ctx, &s)
	if err != nil {
		return nil, err
	}
	s.ThumbnailUrl = info.ThumbnailUrl

	return &s, nil
}

func (b *Broadcaster) HandleStream(s *stream.Stream) *Agent {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, stream.BroadcasterCtxKey{}, b)

	agent := Agent{
		sugar:       b.sugar,
		ctx:         ctx,
		ctxCancel:   cancel,
		Stream:      s,
		ffmpegCmder: b.config.FfmpegCmderCreator(ctx, b.config, s.Id),
		dummyStreamFfmpegCmderCreator: func(ctx context.Context) FfmpegCmder {
			return b.config.DummyStreamFfmpegCmderCreator(ctx, s.Url)
		},
	}

	go agent.Run()

	b.agents[agent.Stream.Id] = &agent

	go func() {
		<-ctx.Done()
		delete(b.agents, agent.Stream.Id)
	}()

	return &agent
}

func (b *Broadcaster) RefreshAgent(streamId int64, newScheduledEndAt time.Time) error {
	a, ok := b.agents[streamId]
	if !ok {
		return errors.New(fmt.Sprintf("Agent for streamId %v not found", streamId))
	}
	a.Stream.ScheduledEndAt = newScheduledEndAt
	return nil
}
