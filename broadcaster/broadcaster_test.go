package broadcaster

import (
	"bytes"
	"context"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"io"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamListener"
	"streamcatch-bot/sc_redis"
	"testing"
)

func TestBroadcaster(t *testing.T) {
	logger := zaptest.NewLogger(t)
	//sugar := logger.Sugar()

	t.Run("ResumeStream", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		scRedisClient := NewTestSCRedisClient()

		ctx := context.Background()
		if err := scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
			StreamId: "stream1",
			StreamJson: string((&sc_redis.RedisStream{
				Id:             "stream1",
				Url:            "http://TEST_URL",
				Platform:       "twitch",
				CreatedAt:      mClock.Now(),
				ScheduledEndAt: mClock.Now(),
				Status:         stream.StatusWaiting,
				ThumbnailUrl:   "http://thumbnail",
				Permanent:      false,
			}).Marshal()),
			AuthorId: "authorId1",
			GuildId:  "guildId1",
			UserId:   "",
		}); err != nil {
			panic(err)
		}
		if err := scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
			StreamId: "stream2",
			StreamJson: string((&sc_redis.RedisStream{
				Id:             "stream2",
				Url:            "http://TEST_URL",
				Platform:       "twitch",
				CreatedAt:      mClock.Now(),
				ScheduledEndAt: mClock.Now(),
				Status:         stream.StatusGoneLive,
				ThumbnailUrl:   "http://thumbnail",
				Permanent:      true,
			}).Marshal()),
			AuthorId: "authorId1",
			GuildId:  "guildId1",
			UserId:   "",
		}); err != nil {
			panic(err)
		}

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				return &testFfmpegCmder{
					ctx: ctx,
				}
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				return &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return nil, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return nil
					},
				},
			},
			Clock:         mClock,
			SCRedisClient: scRedisClient,
		})

		broadcaster.ResumeStreams(func(s *sc_redis.RedisStream) (streamListener.DiscordUpdater, error) {
			return &TestDiscordUpdater{}, nil
		})

		_, ok := broadcaster.Agents()["stream1"]
		assert.True(t, ok)

		stream2Agent, ok := broadcaster.Agents()["stream2"]
		assert.True(t, ok)
		assert.True(t, stream2Agent.Stream.Permanent)
	})

	t.Run("DontResumeStreamIfLocked", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		scRedisClient := NewTestSCRedisClient()

		ctx := context.Background()
		if err := scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
			StreamId: "stream1",
			StreamJson: string((&sc_redis.RedisStream{
				Id:             "stream1",
				Url:            "http://TEST_URL",
				Platform:       "twitch",
				CreatedAt:      mClock.Now(),
				ScheduledEndAt: mClock.Now(),
				Status:         stream.StatusWaiting,
				ThumbnailUrl:   "http://thumbnail",
				Permanent:      false,
			}).Marshal()),
			AuthorId: "authorId1",
			GuildId:  "guildId1",
			UserId:   "",
		}); err != nil {
			panic(err)
		}

		bc1 := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				return &testFfmpegCmder{
					ctx: ctx,
				}
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				return &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return nil, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return nil
					},
				},
			},
			Clock:         mClock,
			SCRedisClient: scRedisClient,
		})

		bc1.ResumeStreams(func(s *sc_redis.RedisStream) (streamListener.DiscordUpdater, error) {
			return &TestDiscordUpdater{}, nil
		})

		_, ok := bc1.Agents()["stream1"]
		assert.True(t, ok)

		bc2 := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				return &testFfmpegCmder{
					ctx: ctx,
				}
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				return &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return nil, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return nil
					},
				},
			},
			Clock:         mClock,
			SCRedisClient: scRedisClient,
		})

		bc2.ResumeStreams(func(s *sc_redis.RedisStream) (streamListener.DiscordUpdater, error) {
			return &TestDiscordUpdater{}, nil
		})

		_, ok = bc2.Agents()["stream1"]
		assert.False(t, ok)
	})

	t.Run("StreamKeepsLockingPeriodically", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		scRedisClient := NewTestSCRedisClient()

		ctx := context.Background()
		if err := scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
			StreamId: "stream1",
			StreamJson: string((&sc_redis.RedisStream{
				Id:             "stream1",
				Url:            "http://TEST_URL",
				Platform:       "twitch",
				CreatedAt:      mClock.Now(),
				ScheduledEndAt: mClock.Now(),
				Status:         stream.StatusWaiting,
				ThumbnailUrl:   "http://thumbnail",
				Permanent:      false,
			}).Marshal()),
			AuthorId: "authorId1",
			GuildId:  "guildId1",
			UserId:   "",
		}); err != nil {
			panic(err)
		}

		bc := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				return &testFfmpegCmder{
					ctx: ctx,
				}
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				return &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return nil, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return nil
					},
				},
			},
			Clock:         mClock,
			SCRedisClient: scRedisClient,
		})

		bc.ResumeStreams(func(s *sc_redis.RedisStream) (streamListener.DiscordUpdater, error) {
			return &TestDiscordUpdater{}, nil
		})

		a := bc.Agents()["stream1"]
		s := a.Stream
	})
}
