package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
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
	TwitchClientId             string
	TwitchClientSecret         string
	TwitchAuthToken            string
	MediaServerRtspHost        string
	MediaServerPublishUser     string
	MediaServerPublishPassword string
	MediaServerApiUrl          string
}

func New(sugar *zap.SugaredLogger, cfg *Config) *Broadcaster {
	helixClient, err := helix.NewClient(&helix.Options{
		ClientID:     cfg.TwitchClientId,
		ClientSecret: cfg.TwitchClientSecret,
	})
	if err != nil {
		sugar.Panicw("Failed to create helix client", "error", err)
	}

	// TODO: handle token refresh
	resp, err := helixClient.RequestAppAccessToken([]string{"user:read:email"})
	if err != nil {
		sugar.Panicw("Failed to get twitch app access token", "error", err)
	}
	// Set the access token on the client
	helixClient.SetAppAccessToken(resp.Data.AccessToken)

	b := Broadcaster{
		sugar:  sugar,
		agents: make(map[int64]*Agent),
		helix:  helixClient,
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
	GoneOnline     bool
	Listener       StreamStatusListener
}

type StreamStatus int

const (
	StreamStarted StreamStatus = iota
	GoneLive
	ForceStopped
	Ended
	Timeout
)

type StreamStatusListener interface {
	Status(stream *Stream, status StreamStatus)
	Close(stream *Stream, reason error)
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

	// if platform == "" {
	// 	return nil, errInvalidUrl
	// }
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
		stream:      stream,
		redirectUrl: stream.Url,
	}

	go agent.Run()

	b.agents[agent.stream.Id] = &agent

	go func() {
		<-ctx.Done()
		delete(b.agents, agent.stream.Id)
	}()

	return &agent
}

func (b *Broadcaster) RefreshAgent(streamId int64, newScheduledEndAt time.Time) error {
	a, ok := b.agents[streamId]
	if !ok {
		return errors.New(fmt.Sprintf("Agent for streamId %v not found", streamId))
	}
	a.stream.ScheduledEndAt = newScheduledEndAt
	return nil
}
