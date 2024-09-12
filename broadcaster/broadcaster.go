package broadcaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
	"os/exec"
	"streamcatch-bot/broadcaster/bc_config"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamListener"
	"streamcatch-bot/sc_redis"
	"strings"
	"time"
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
	FfmpegCmderCreator            func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder
	DummyStreamFfmpegCmderCreator func(ctx context.Context, streamUrl string) FfmpegCmder
	StreamAvailableChecker        func(streamId stream.Id) (bool, error)
	Helix                         *helix.Client
	StreamPlatforms               map[name.Name]stream.Platform
	Clock                         quartz.Clock
	StreamerInfoFetcher           func(ctx context.Context, s *stream.Stream) (*stream.Info, error)
	SCRedisClient                 sc_redis.SCRedisClient
}

type Broadcaster struct {
	sugar *zap.SugaredLogger
	// TODO: persist this
	agents map[stream.Id]*Agent
	helix  *helix.Client
	Config *Config
}

func (b *Broadcaster) Agents() map[stream.Id]*Agent {
	return b.agents
}

func (b *Broadcaster) MediaServerPublishUser() string {
	return b.Config.MediaServerPublishUser
}

func (b *Broadcaster) MediaServerPublishPassword() string {
	return b.Config.MediaServerPublishPassword
}

func New(sugar *zap.SugaredLogger, cfg *Config) *Broadcaster {
	b := Broadcaster{
		sugar:  sugar,
		agents: make(map[stream.Id]*Agent),
		helix:  cfg.Helix,
		Config: cfg,
	}

	return &b
}

func (b *Broadcaster) MakeLocalStream(ctx context.Context, url string, listener stream.StatusListener, permanent bool) (*stream.Stream, error) {
	id, err := gonanoid.New()
	if err != nil {
		return nil, err
	}
	mutex := &LocalMutex{}
	if err := mutex.Lock(); err != nil {
		panic(err)
	}
	clock := b.Config.Clock
	s := stream.Stream{
		Id:             stream.Id(id),
		Url:            url,
		Platform:       platform.Local,
		CreatedAt:      clock.Now(),
		ScheduledEndAt: clock.Now().Add(bc_config.ScheduledEndDuration),
		Listener:       listener,
		Permanent:      permanent,
		Mutex:          mutex,
	}

	info, err := b.Config.StreamerInfoFetcher(ctx, &s)
	if err != nil {
		return nil, err
	}
	s.ThumbnailUrl = info.ThumbnailUrl

	return &s, nil
}

func (b *Broadcaster) MakeStream(ctx context.Context, url string, listener stream.StatusListener, permanent bool) (*stream.Stream, error) {
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

	id, err := gonanoid.New()
	if err != nil {
		return nil, err
	}

	mutex := b.Config.SCRedisClient.StreamMutex(id)
	if err := mutex.Lock(); err != nil {
		panic(err)
	}

	clock := b.Config.Clock
	s := stream.Stream{
		Id:             stream.Id(id),
		Url:            url,
		Platform:       platformName,
		CreatedAt:      clock.Now(),
		ScheduledEndAt: clock.Now().Add(bc_config.ScheduledEndDuration),
		Listener:       listener,
		Permanent:      permanent,
		Mutex:          mutex,
	}

	info, err := b.Config.StreamerInfoFetcher(ctx, &s)
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
		sugar:     b.sugar,
		ctx:       ctx,
		ctxCancel: cancel,
		Stream:    s,
		ffmpegCmder: func(ctx context.Context) FfmpegCmder {
			return b.Config.FfmpegCmderCreator(ctx, b.Config, s.Id)
		},
		dummyStreamFfmpegCmderCreator: func(ctx context.Context) FfmpegCmder {
			return b.Config.DummyStreamFfmpegCmderCreator(ctx, s.Url)
		},
	}

	go agent.Run()

	b.agents[agent.Stream.Id] = &agent

	if !s.Permanent {
		go func() {
			<-ctx.Done()
			delete(b.agents, agent.Stream.Id)
		}()
	}

	return &agent
}

func (b *Broadcaster) RefreshAgent(streamId stream.Id, newScheduledEndAt time.Time) error {
	a, ok := b.agents[streamId]
	if !ok {
		return errors.New(fmt.Sprintf("Agent for streamId %v not found", streamId))
	}
	a.Stream.ScheduledEndAt = newScheduledEndAt
	err := sc_redis.PersistStream(b.Config.SCRedisClient, a.Stream)
	if err != nil {
		return err
	}
	return nil
}

func (b *Broadcaster) ResumeStream(redisStream *sc_redis.RedisStream, discordUpdater streamListener.DiscordUpdater) {
	sl := streamListener.StreamListener{
		Sugar:          b.sugar,
		DiscordUpdater: discordUpdater,
		SCRedisClient:  b.Config.SCRedisClient,
	}
	s := stream.Stream{
		Id:                   stream.Id(redisStream.Id),
		Url:                  redisStream.Url,
		Platform:             redisStream.Platform,
		CreatedAt:            redisStream.CreatedAt,
		ScheduledEndAt:       redisStream.ScheduledEndAt,
		Status:               redisStream.Status,
		Listener:             &sl,
		ThumbnailUrl:         redisStream.ThumbnailUrl,
		Permanent:            redisStream.Permanent,
		PlatformLastStreamId: redisStream.PlatformLastStreamId,
	}
	b.HandleStream(&s)
	b.sugar.Infof("Resumed stream %s", s.Id)
}

func (b *Broadcaster) ResumeStreams(discordUpdaterCreator func(s *sc_redis.RedisStream) (streamListener.DiscordUpdater, error)) {
	scRedisClient := b.Config.SCRedisClient
	ctx := context.Background()
	streams, err := scRedisClient.GetStreams(ctx)
	if err != nil {
		panic(err)
	}
	// TODO: continue polling for unhandled streams in db in case lock expires
	for streamId, streamJson := range streams {
		// Obtain right to handle stream
		mutex := scRedisClient.StreamMutex(streamId)
		err := mutex.Lock()
		if err != nil {
			b.sugar.Debugw("Stream locked, not handling", "streamId", streamId)
			continue
		}

		var s sc_redis.RedisStream
		err = json.Unmarshal([]byte(streamJson), &s)
		if err != nil {
			b.sugar.Errorf("Could not unmarshal stream %s, deleting", streamId)
			err := scRedisClient.CleanupStream(ctx, streamId)
			if err != nil {
				panic(err)
			}
			continue
		}

		discordUpdater, err := discordUpdaterCreator(&s)
		if err != nil {
			b.sugar.Errorf("Could not create discord updater for stream %s, deleting", streamId)
			err := scRedisClient.CleanupStream(ctx, streamId)
			if err != nil {
				panic(err)
			}
			continue
		}
		b.ResumeStream(&s, discordUpdater)
	}
}
