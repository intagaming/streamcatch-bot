package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
	"os"
	"time"
)

type Broadcaster struct {
	sugar                      *zap.SugaredLogger
	agents                     map[int64]*Agent
	helix                      *helix.Client
	twitchAuthToken            string
	mediaServerRtspHost        string
	mediaServerPublishUser     string
	mediaServerPublishPassword string
	mediaServerApiUrl          string
}

func (b *Broadcaster) Agents() map[int64]*Agent {
	return b.agents
}

func (b *Broadcaster) MediaServerPublishUser() string {
	return b.mediaServerPublishUser
}

func (b *Broadcaster) MediaServerPublishPassword() string {
	return b.mediaServerPublishPassword
}

type broadcasterCtxKey struct{}

func New(sugar *zap.SugaredLogger) *Broadcaster {
	var twitchClientId = os.Getenv("TWITCH_CLIENT_ID")
	if twitchClientId == "" {
		sugar.Panic("TWITCH_CLIENT_ID is not set")
	}
	var twitchClientSecret = os.Getenv("TWITCH_CLIENT_SECRET")
	if twitchClientSecret == "" {
		sugar.Panic("TWITCH_CLIENT_SECRET is not set")
	}
	helixClient, err := helix.NewClient(&helix.Options{
		ClientID:     twitchClientId,
		ClientSecret: twitchClientSecret,
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

	var twitchAuthToken = os.Getenv("TWITCH_AUTH_TOKEN")
	if twitchAuthToken == "" {
		sugar.Warn("TWITCH_AUTH_TOKEN is not set")
	}

	mediaServerRtspHost := os.Getenv("MEDIA_SERVER_RTSP_HOST")
	if mediaServerRtspHost == "" {
		sugar.Panic("MEDIA_SERVER_RTSP_HOST is not set")
	}
	mediaServerPublishUser := os.Getenv("MEDIA_SERVER_PUBLISH_USER")
	if mediaServerPublishUser == "" {
		sugar.Panic("MEDIA_SERVER_PUBLISH_USER is not set")
	}
	mediaServerPublishPassword := os.Getenv("MEDIA_SERVER_PUBLISH_PASSWORD")
	if mediaServerPublishPassword == "" {
		sugar.Panic("MEDIA_SERVER_PUBLISH_PASSWORD is not set")
	}
	mediaServerApiUrl := os.Getenv("MEDIA_SERVER_API_URL")
	if mediaServerApiUrl == "" {
		sugar.Panic("MEDIA_SERVER_API_URL is not set")
	}

	b := Broadcaster{
		sugar:                      sugar,
		agents:                     make(map[int64]*Agent),
		helix:                      helixClient,
		twitchAuthToken:            twitchAuthToken,
		mediaServerRtspHost:        mediaServerRtspHost,
		mediaServerPublishUser:     mediaServerPublishUser,
		mediaServerPublishPassword: mediaServerPublishPassword,
		mediaServerApiUrl:          mediaServerApiUrl,
	}

	return &b
}

type Stream struct {
	Id             int64
	UserId         string
	Url            string
	Platform       string
	CreatedAt      time.Time
	ScheduledEndAt time.Time
	TerminatedAt   time.Time
	GoneOnline     bool
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
