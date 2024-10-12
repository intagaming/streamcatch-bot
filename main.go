package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/coder/quartz"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/nicklaw5/helix/v2"
	goredislib "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"streamcatch-bot/broadcaster"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamlistener"
	"streamcatch-bot/discord"
	"streamcatch-bot/scredis"
	"strings"
	"time"
)

var isDev bool

const (
	devUsage = "whether to run in dev mode, which prints debug logs"
)

func init() {
	flag.BoolVar(&isDev, "dev", false, devUsage)
	flag.BoolVar(&isDev, "d", false, devUsage+" (shorthand)")
}

func main() {
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())

	var logger *zap.Logger
	var err error
	if isDev {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			log.Printf("failed to sync logger: %v", err)
		}
	}(logger)
	sugar := logger.Sugar()

	if _, err := exec.LookPath("streamlink"); err != nil {
		sugar.Panic("streamlink not found")
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		sugar.Panic("ffmpeg not found")
	}

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
	mediaServerPlaybackUrl := os.Getenv("MEDIA_SERVER_PLAYBACK_URL")
	if mediaServerPlaybackUrl == "" {
		sugar.Panic("MEDIA_SERVER_PLAYBACK_URL is not set")
	}
	mediaServerApiUrl := os.Getenv("MEDIA_SERVER_API_URL")
	if mediaServerApiUrl == "" {
		sugar.Panic("MEDIA_SERVER_API_URL is not set")
	}
	var twitchAuthToken = os.Getenv("TWITCH_AUTH_TOKEN")
	if twitchAuthToken == "" {
		sugar.Warn("TWITCH_AUTH_TOKEN is not set")
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		sugar.Warn("REDIS_ADDR is not set")
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")
	useNvidiaGpu := os.Getenv("NVIDIA_GPU")

	rdb := goredislib.NewClient(&goredislib.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       0,
	})
	pool := goredis.NewPool(rdb)
	rs := redsync.New(pool)

	var scRedisClient scredis.Client = &scredis.RealClient{Redis: rdb, Redsync: rs}

	// Recordings server
	err = os.MkdirAll("recordings", os.ModePerm)
	if err != nil {
		sugar.Fatalf("could not create recordings folder: %v", err)
	}
	recordingsFs := http.FileServer(http.Dir("recordings"))
	http.Handle("/recordings/", http.StripPrefix("/recordings/", neuter(recordingsFs)))
	clock := quartz.NewReal()
	clock.TickerFunc(ctx, time.Minute, func() error {
		sugar.Debugf("Removing old recordings")

		cutoff := clock.Now().Add(-24 * time.Hour)
		err := filepath.Walk("recordings", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && info.ModTime().Before(cutoff) {
				if err := os.Remove(path); err != nil {
					sugar.Errorf("Failed to remove %s: %v", path, err)
				}
			}
			return nil
		})

		if err != nil {
			sugar.Errorf("Error walking old recordings dir: %v", err)
		}
		return nil
	})

	var bot *discord.Bot
	bc := broadcaster.New(sugar, &broadcaster.Config{
		TwitchClientId:                twitchClientId,
		TwitchClientSecret:            twitchClientSecret,
		TwitchAuthToken:               twitchAuthToken,
		MediaServerRtspHost:           mediaServerRtspHost,
		MediaServerPublishUser:        mediaServerPublishUser,
		MediaServerPublishPassword:    mediaServerPublishPassword,
		MediaServerPlaybackUrl:        mediaServerPlaybackUrl,
		MediaServerApiUrl:             mediaServerApiUrl,
		FfmpegCmderCreator:            broadcaster.NewRealFfmpegCmder,
		DummyStreamFfmpegCmderCreator: broadcaster.NewRealDummyStreamFfmpegCmder,
		StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
			resp, err := http.Get(mediaServerApiUrl + "/v3/paths/get/" + string(streamId))
			if err != nil {
				return false, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return false, errors.New("response was not 200 but " + resp.Status)
			}
			return true, nil
		},
		StreamPlatforms: map[name.Name]stream.Platform{
			platform.Twitch: &platform.TwitchStreamPlatform{
				HelixClient:     helixClient,
				TwitchAuthToken: twitchAuthToken,
			},
			platform.YouTube: &platform.YoutubeStreamPlatform{},
			platform.Generic: &platform.GenericStreamPlatform{},
			platform.Local:   &platform.LocalStreamPlatform{},
		},
		StreamerInfoFetcher: func(ctx context.Context, s *stream.Stream) (*stream.Info, error) {
			switch s.Platform {
			case platform.Twitch:
				info, err := platform.FetchTwitchStreamerInfo(helixClient, s.Url)
				if err != nil {
					return nil, err
				}
				return &stream.Info{ThumbnailUrl: info.ProfileImageURL}, nil
			default:
				return &stream.Info{}, nil
			}
		},
		Clock:         clock,
		SCRedisClient: scRedisClient,
		DiscordUpdaterCreator: func(s *scredis.RedisStream) (streamlistener.DiscordUpdater, error) {
			ctx := context.Background()
			channelID, err := scRedisClient.GetStreamChannelID(ctx, s.Id)
			if err != nil {
				return nil, err
			}
			var message *discordgo.Message
			messageJson, err := scRedisClient.GetStreamMessage(ctx, s.Id)
			if err == nil {
				message = &discordgo.Message{}
				err = message.UnmarshalJSON([]byte(messageJson))
				if err != nil {
					return nil, err
				}
			}
			var authorId string
			authorId, err = scRedisClient.GetStreamAuthorId(ctx, s.Id)
			if err != nil {
				return nil, err
			}

			return &discord.RealDiscordUpdater{
				Bot:       bot,
				ChannelID: channelID,
				Message:   message,
				AuthorId:  authorId,
			}, nil
		},
		UseNvidiaGpu: useNvidiaGpu == "1",
	})

	bot = discord.New(sugar, bc, scRedisClient)

	// get streams from db and handle
	sugar.Info("Resuming streams...")
	bc.ResumeStreams()
	bc.ResumeStreamPoller(ctx)

	if isDev {
		http.HandleFunc("/local/new", func(w http.ResponseWriter, r *http.Request) {
			permanent := r.URL.Query().Get("permanent") == "true"
			s, err := bc.MakeLocalStream(context.Background(), r.URL.Query().Get("url"), &localStreamListener{}, permanent)
			if err != nil {
				panic(err)
			}
			bc.HandleStream(s)
			_, _ = w.Write([]byte(fmt.Sprintf("%s", s.Id)))
		})
		http.HandleFunc("/local/online", func(w http.ResponseWriter, r *http.Request) {
			streamId := stream.Id(r.URL.Query().Get("id"))
			platform.SetLocalOnline(streamId)
		})
		http.HandleFunc("/local/stop", func(w http.ResponseWriter, r *http.Request) {
			streamId := stream.Id(r.URL.Query().Get("id"))
			a, ok := bc.Agents()[streamId]
			if !ok {
				sugar.Debugw("Agent not found", "streamId", streamId)
				w.WriteHeader(http.StatusNotFound)
				return
			}

			a.Close(stream.ReasonForceStopped, nil)
		})
	}

	go func() {
		_ = http.ListenAndServe(":8080", nil)
	}()

	bot.Init()

	sugar.Info("Initialization complete.")

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt)
	<-sigch

	cancel()

	err = bot.Close()
	if err != nil {
		sugar.Infof("could not close session gracefully: %s", err)
	}
}

type localStreamListener struct{}

func (l *localStreamListener) Status(*stream.Stream) {
}

func (l *localStreamListener) StreamStarted(*stream.Stream) {
}

func (l *localStreamListener) Close(*stream.Stream) {
}

func neuter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}
