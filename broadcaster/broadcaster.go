package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
	"io"
	"os/exec"
	"strings"
	"time"
)

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

type broadcasterCtxKey struct{}

type Config struct {
	TwitchClientId                string
	TwitchClientSecret            string
	TwitchAuthToken               string
	MediaServerRtspHost           string
	MediaServerPublishUser        string
	MediaServerPublishPassword    string
	MediaServerApiUrl             string
	FfmpegCmderCreator            func(ctx context.Context, config *Config, streamId int64) FfmpegCmder
	DummyStreamFfmpegCmderCreator func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder
	StreamAvailableChecker        func(streamId int64) (bool, error)
	StreamWaiter                  func(agent *Agent) error
	Helix                         *helix.Client
	Streamer                      func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error
	Clock                         quartz.Clock
}

func New(sugar *zap.SugaredLogger, cfg *Config) *Broadcaster {
	b := Broadcaster{
		sugar:  sugar,
		agents: make(map[int64]*Agent),
		helix:  cfg.Helix,
		config: cfg,
	}

	return &b
}

type Stream struct {
	Id             int64
	Url            string
	Platform       string
	CreatedAt      time.Time
	ScheduledEndAt time.Time
	TerminatedAt   time.Time
	Status         StreamStatus
	EndedReason    *EndedReason
	EndedError     error
	Listener       StreamStatusListener
}

type StreamStatus int

const (
	Waiting StreamStatus = iota
	GoneLive
	Ended
)

type EndedReason int

const (
	ForceStopped EndedReason = iota
	Timeout
	StreamEnded
	Fulfilled
	Errored
)

type StreamStatusListener interface {
	Status(stream *Stream)
	StreamStarted(stream *Stream)
	Close(stream *Stream)
}

var (
	errInvalidUrl = errors.New("invalid stream url")
)

var idCount int64 = 0

const (
	ScheduledEndDuration = 30 * time.Minute
)

func MakeStream(ctx context.Context, url string, listener StreamStatusListener) (*Stream, error) {
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

	var platform string
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		platform = "youtube"
	} else if strings.Contains(url, "twitch.tv") {
		platform = "twitch"
	} else {
		platform = "generic"
	}

	idCount += 1
	return &Stream{
		Id:             idCount,
		Url:            url,
		Platform:       platform,
		CreatedAt:      time.Now(),
		ScheduledEndAt: time.Now().Add(ScheduledEndDuration),
		Listener:       listener,
	}, nil
}

func (b *Broadcaster) HandleStream(stream *Stream) *Agent {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, broadcasterCtxKey{}, b)

	agent := Agent{
		sugar:       b.sugar,
		ctx:         ctx,
		ctxCancel:   cancel,
		Stream:      stream,
		ffmpegCmder: b.config.FfmpegCmderCreator(ctx, b.config, stream.Id),
		dummyStreamFfmpegCmderCreator: func(ctx context.Context) DummyStreamFfmpegCmder {
			return b.config.DummyStreamFfmpegCmderCreator(ctx, stream.Url)
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

func NewRealStreamWaiter(sugar *zap.SugaredLogger, helixClient *helix.Client, a *Agent) error {
	var err error
	if a.Stream.Platform == "twitch" {
		err = WaitForTwitchOnline(sugar, a.ctx, helixClient, a.Stream)
	} else if a.Stream.Platform == "youtube" {
		_, err = WaitForYoutubeOnline(sugar, a.ctx, a.Stream)
	} else if a.Stream.Platform == "generic" {
		_, err = WaitForGenericOnline(sugar, a.ctx, a.Stream)
	} else {
		return errors.New("Unknown platform: " + a.Stream.Platform)
	}
	return err
}
