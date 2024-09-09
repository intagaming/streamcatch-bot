package broadcaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"io"
	"streamcatch-bot/broadcaster/bc_config"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamListener"
	"streamcatch-bot/sc_redis"
	sc_redis_test "streamcatch-bot/sc_redis/sc_redis_test"
	"sync"
	"testing"
	"time"
)

type TestDiscordUpdater struct{}

func (t *TestDiscordUpdater) UpdateStreamCatchMessage(*stream.Stream) {
}

type testFfmpegCmder struct {
	ctx     context.Context
	stdin   *io.PipeReader
	started bool
}

func (t *testFfmpegCmder) SetStdin(pipe *io.PipeReader) {
	t.stdin = pipe
}

func (t *testFfmpegCmder) SetStdout(io.Writer) {}

func (t *testFfmpegCmder) SetStderr(io.Writer) {
}

func (t *testFfmpegCmder) Start() error {
	t.started = true
	return t.ctx.Err()
}

func (t *testFfmpegCmder) Wait() error {
	<-t.ctx.Done()
	return t.ctx.Err()
}

type testDummyStreamFfmpegCmder struct {
	ctx             context.Context
	stdout          io.Writer
	started         bool
	waitToStart     bool
	waitToStartChan chan struct{}
}

func (t *testDummyStreamFfmpegCmder) SetStdin(*io.PipeReader) {
}

func (t *testDummyStreamFfmpegCmder) SetStdout(pipe io.Writer) {
	t.stdout = pipe
}

func (t *testDummyStreamFfmpegCmder) SetStderr(io.Writer) {
}

func (t *testDummyStreamFfmpegCmder) Start() error {
	t.started = true
	if t.waitToStart {
		select {
		case <-t.waitToStartChan:
		case <-t.ctx.Done():
		}
	}
	return t.ctx.Err()
}

func (t *testDummyStreamFfmpegCmder) Wait() error {
	<-t.ctx.Done()
	return t.ctx.Err()
}

type TestTwitchPlatform struct {
	waitForOnline func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error)
	stream        func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error
}

func (t *TestTwitchPlatform) WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
	return t.waitForOnline(sugar, ctx, s)
}

func (t *TestTwitchPlatform) Stream(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
	return t.stream(ctx, s, pipeWrite, streamlinkErrBuf, ffmpegErrBuf)
}

func NewTestSCRedisClient() sc_redis.SCRedisClient {
	var scRedisClient sc_redis.SCRedisClient = &sc_redis_test.TestSCRedisClient{
		Streams:        make(map[string]string),
		StreamMutexMap: make(map[string]stream.Mutex),
		StreamAuthorId: make(map[string]string),
		StreamGuildId:  make(map[string]string),
		GuildStreams:   make(map[string]map[string]struct{}),
		StreamUserId:   make(map[string]string),
		UserStreams:    make(map[string]map[string]struct{}),
		StreamMessage:  make(map[string]string),
	}
	return scRedisClient
}

func GetRedisStream(t *testing.T, scRedisClient sc_redis.SCRedisClient, streamId string) sc_redis.RedisStream {
	streamJson, err := scRedisClient.GetStream(context.Background(), streamId)
	assert.Nil(t, err)
	var redisStream sc_redis.RedisStream
	err = json.Unmarshal([]byte(streamJson), &redisStream)
	assert.Nil(t, err)
	return redisStream
}

func AssertRedisStreamPersisted(t *testing.T, scRedisClient sc_redis.SCRedisClient, s *stream.Stream) {
	streamJson, err := scRedisClient.GetStream(context.Background(), string(s.Id))
	assert.Nil(t, err)
	assert.Equal(t, streamJson, string(sc_redis.RedisStreamFromStream(s).Marshal()))
}

func setupTestStream(scRedisClient sc_redis.SCRedisClient, s *stream.Stream) {
	err := s.Mutex.Lock()
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	err = scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
		StreamId:   string(s.Id),
		StreamJson: string(sc_redis.RedisStreamFromStream(s).Marshal()),
		AuthorId:   "testAuthorId",
		GuildId:    "testGuildId",
		UserId:     "",
	})
	if err != nil {
		panic(err)
	}
}

func TestAgent(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sugar := logger.Sugar()

	advance := func(mClock *quartz.Mock, desired time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			p, ok := mClock.Peek()
			if !ok || p > desired {
				mClock.Advance(desired).MustWait(ctx)
				<-time.After(10 * time.Millisecond)
				break
			}
			mClock.Advance(p).MustWait(ctx)
			desired -= p
			// Give time for agent's goroutine to run logic
			<-time.After(10 * time.Millisecond)
		}
	}

	advanceUntilCond := func(mClock *quartz.Mock, cond func() bool, limitDuration time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			p, ok := mClock.Peek()
			if !ok || p > limitDuration {
				mClock.Advance(limitDuration).MustWait(ctx)
				<-time.After(10 * time.Millisecond)
				break
			}
			mClock.Advance(p).MustWait(ctx)
			limitDuration -= p
			// Give time for agent's goroutine to run logic
			<-time.After(10 * time.Millisecond)
			if cond() {
				return
			}
		}
		assert.True(t, cond())
	}

	newStreamPollerTrap := func(mClock *quartz.Mock) *quartz.Trap {
		return mClock.Trap().TickerFunc("StreamPoller")
	}

	waitForTrap := func(trap *quartz.Trap) {
		call, err := trap.Wait(context.Background())
		if err != nil {
			panic(err)
		}
		call.Release()
	}

	t.Run("HappyCase", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerData := []byte{69, 110, 105, 99, 101}
		streamEndChan := make(chan struct{}, 1)
		streamerRetryUntilSuccess := 2

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						if streamerRetryUntilSuccess > 0 {
							streamerRetryUntilSuccess -= 1
							return errors.New("stream not available")
						}
						_, err := pipeWrite.Write(streamerData)
						if err != nil {
							return fmt.Errorf("unexpected streamer write error: %w", err)
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-streamEndChan:
						}
						return nil
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		streamPollerTrap := newStreamPollerTrap(mClock)
		defer streamPollerTrap.Close()

		broadcaster.HandleStream(&s)

		waitForTrap(streamPollerTrap)

		// expect StreamCatch stream available
		assert.False(t, s.SCStreamStarted)

		streamAvailableChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		// expect ffmpeg stream to receive dummy stream outputs
		dummyOutData := []byte{1, 2, 3, 4, 5}
		dummyWriteWg := sync.WaitGroup{}
		dummyWriteWg.Add(1)
		var dummyWriteErr error
		go func() {
			_, err := dummyFfmpegCmder.stdout.Write(dummyOutData)
			dummyWriteErr = err
			dummyWriteWg.Done()
		}()

		ffmpegCmderIn := make([]byte, len(dummyOutData))
		_, err := ffmpegCmder.stdin.Read(ffmpegCmderIn)
		if err != nil {
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", err)
		}

		dummyWriteWgWaitDoneChan := make(chan struct{})
		go func() {
			dummyWriteWg.Wait()
			dummyWriteWgWaitDoneChan <- struct{}{}
		}()
		select {
		case <-dummyWriteWgWaitDoneChan:
		case <-time.After(time.Second):
			t.Fatal("Dummy write to ffmpeg never ended")
		}
		if dummyWriteErr != nil {
			t.Errorf("Failed to write dummy data: %v", err)
		}

		assert.Equal(t, dummyOutData, ffmpegCmderIn)

		// expect StreamCatch gone online ok
		assert.NotEqual(t, stream.StatusGoneLive, s.Status)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		// expect ffmpegCmder to receive stream data from Streamer
		var streamReadErr error
		ffmpegCmderIn = make([]byte, len(streamerData))
		isRead := false
		go func() {
			_, streamReadErr = ffmpegCmder.stdin.Read(ffmpegCmderIn)
			isRead = true
		}()
		advanceUntilCond(mClock, func() bool {
			return isRead
		}, 30*time.Second)

		if streamReadErr != nil {
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", streamReadErr)
		}

		assert.Equal(t, streamerData, ffmpegCmderIn)

		assert.NotEqual(t, stream.StatusEnded, s.Status)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusEnded
		}, s.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, s.EndedReason)
		assert.Equal(t, stream.ReasonFulfilled, *s.EndedReason)
	})

	t.Run("StreamNeverCameOnline", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return true, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return &name.WaitForOnlineData{}, errors.New("should not go here")
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return errors.New("should not be called")
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		streamPollerTrap := newStreamPollerTrap(mClock)
		defer streamPollerTrap.Close()

		broadcaster.HandleStream(&s)

		waitForTrap(streamPollerTrap)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusEnded
		}, s.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, s.EndedReason)
		assert.Equal(t, stream.ReasonTimeout, *s.EndedReason)
	})

	t.Run("StreamOnlineThenOfflineImmediatelyThenNeverCameBackOn", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerEndChan := make(chan struct{}, 1)
		streamerCalledTime := 0

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return true, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil

					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						streamerCalledTime += 1
						if streamerCalledTime <= 1 {
							<-streamerEndChan
						}
						return nil
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		streamPollerTrap := newStreamPollerTrap(mClock)
		defer streamPollerTrap.Close()

		broadcaster.HandleStream(&s)

		waitForTrap(streamPollerTrap)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		assert.Equal(t, stream.StatusWaiting, s.Status)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, time.Second)

		streamerEndChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return streamerCalledTime >= MaxRetries+1
		}, 3*time.Minute)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusEnded
		}, time.Second)

		assert.NotNil(t, s.EndedReason)
		assert.Equal(t, stream.ReasonErrored, *s.EndedReason)
		assert.Equal(t, FailedToStreamError, s.EndedError)
	})

	t.Run("StreamOnlineThenOfflineThenCameBackOn", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerEndChan := make(chan struct{}, 1)
		streamerCalledTime := 0

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return true, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						streamerCalledTime += 1
						<-streamerEndChan
						return nil
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		streamPollerTrap := newStreamPollerTrap(mClock)
		defer streamPollerTrap.Close()

		broadcaster.HandleStream(&s)

		waitForTrap(streamPollerTrap)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		assert.Equal(t, stream.StatusWaiting, s.Status)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, time.Second)

		streamerEndChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return streamerCalledTime == 2
		}, time.Minute)

		streamerEndChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return streamerCalledTime == 3
		}, time.Minute)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusEnded
		}, s.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, s.EndedReason)
		assert.Equal(t, stream.ReasonFulfilled, *s.EndedReason)
	})

	t.Run("StreamCameOnlineBeforeStreamCatchReady", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return errors.New("should not go here")
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		broadcaster.HandleStream(&s)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		assert.False(t, s.SCStreamStarted)

		streamAvailableChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		// We're happy if there's no runtime error so far.
	})

	t.Run("StreamCatchStoppedBeforeDummyStreamStarts", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx:         ctx,
					waitToStart: true,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return errors.New("should not go here")
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		agent := broadcaster.HandleStream(&s)

		agent.Close(stream.ReasonForceStopped, nil)

		// We're happy if there's no runtime error so far.
	})

	t.Run("PermanentStreamHappyCase", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		var ffmpegCmder *testFfmpegCmder
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		//streamGoneOnlineIdChan := make(chan string, 1)
		streamGoneOnlineId := "stream1"
		streamerData := []byte{69, 110, 105, 99, 101}
		streamEndChan := make(chan struct{}, 1)

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder = &testFfmpegCmder{
					ctx: ctx,
				}
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				if GetRedisStream(t, scRedisClient, string(streamId)).Status == stream.StatusGoneLive {
					return true, nil
				}
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{StreamId: streamGoneOnlineId}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						_, err := pipeWrite.Write(streamerData)
						if err != nil {
							return fmt.Errorf("unexpected streamer write error: %w", err)
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-streamEndChan:
						}
						return nil
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: time.Time{},
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
			Permanent:      true,
		}

		setupTestStream(scRedisClient, &s)

		broadcaster.HandleStream(&s)

		assert.Equal(t, time.Time{}, s.ScheduledEndAt)

		// assert that stream won't be closed because timeout
		advance(mClock, 30*time.Second)
		assert.Nil(t, s.EndedReason)

		// Stream should have not started yet
		advance(mClock, 1*time.Minute)
		assert.False(t, s.SCStreamStarted)

		// assert ffmpeg cmd not started (not sending data to media server)
		assert.Nil(t, ffmpegCmder)
		assert.Nil(t, dummyFfmpegCmder)

		// Stream gone online
		assert.NotEqual(t, s.Status, stream.StatusGoneLive)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		// Make sure ScheduledEndAt is set
		assert.NotEqual(t, time.Time{}, s.ScheduledEndAt)

		// expect ffmpegCmder to receive stream data from Streamer
		var streamReadErr error
		ffmpegCmderIn := make([]byte, len(streamerData))
		isRead := false
		go func() {
			_, streamReadErr = ffmpegCmder.stdin.Read(ffmpegCmderIn)
			isRead = true
		}()
		advanceUntilCond(mClock, func() bool {
			return isRead
		}, 30*time.Second)

		if streamReadErr != nil {
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", streamReadErr)
		}

		assert.Equal(t, streamerData, ffmpegCmderIn)

		assert.NotEqual(t, s.Status, stream.StatusWaiting)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusWaiting
		}, s.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, s.EndedReason)
		assert.Equal(t, stream.ReasonFulfilled, *s.EndedReason)

		assert.Equal(t, time.Time{}, s.ScheduledEndAt)

		// make sure stream is still listening
		advance(mClock, time.Minute)

		streamGoneOnlineChan <- struct{}{}

		// Stream should not be handled because it's already catch
		advance(mClock, time.Minute)
		assert.Equal(t, stream.StatusWaiting, s.Status)

		// Now new stream appears
		streamGoneOnlineId = "stream2"
		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)
	})

	t.Run("PermanentStreamForceClosedShouldNotContinue", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		var ffmpegCmder *testFfmpegCmder
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder = &testFfmpegCmder{
					ctx: ctx,
				}
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-ctx.Done()
						return &name.WaitForOnlineData{}, errors.New("should not go here")
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						return errors.New("should not be called")
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: time.Time{},
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
			Permanent:      true,
		}

		setupTestStream(scRedisClient, &s)

		a := broadcaster.HandleStream(&s)

		a.Close(stream.ReasonForceStopped, nil)

		assert.NotNil(t, a.ctx.Err())
		assert.Equal(t, stream.StatusEnded, s.Status)
	})

	t.Run("PersistedCorrectly", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamEndChan := make(chan struct{}, 1)

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-streamEndChan:
						}
						return nil
					},
				},
			},
			Clock:         mClock,
			SCRedisClient: scRedisClient,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(bc_config.ScheduledEndDuration),
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
		}

		setupTestStream(scRedisClient, &s)

		streamPollerTrap := newStreamPollerTrap(mClock)
		defer streamPollerTrap.Close()

		a := broadcaster.HandleStream(&s)

		waitForTrap(streamPollerTrap)

		// expect StreamCatch stream available
		assert.False(t, s.SCStreamStarted)

		streamAvailableChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		AssertRedisStreamPersisted(t, scRedisClient, &s)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return GetRedisStream(t, scRedisClient, string(s.Id)).Status == stream.StatusGoneLive
		}, 5*time.Second)

		err := broadcaster.RefreshAgent(s.Id, s.ScheduledEndAt.Add(time.Hour))
		assert.Nil(t, err)

		AssertRedisStreamPersisted(t, scRedisClient, &s)

		a.Close(stream.ReasonForceStopped, nil)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusEnded
		}, 5*time.Second)

		streamJson, err := scRedisClient.GetStream(context.Background(), string(s.Id))
		assert.Nil(t, err)
		assert.Empty(t, streamJson)
	})

	t.Run("PermanentStreamPersistedCorrectly", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		var ffmpegCmder *testFfmpegCmder
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamGoneOnlineId := "stream1"

		scRedisClient := NewTestSCRedisClient()
		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder {
				ffmpegCmder = &testFfmpegCmder{
					ctx: ctx,
				}
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) FfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId stream.Id) (bool, error) {
				if GetRedisStream(t, scRedisClient, string(streamId)).Status == stream.StatusGoneLive {
					return true, nil
				}
				return false, nil
			},
			StreamPlatforms: map[name.Name]stream.Platform{
				platform.Twitch: &TestTwitchPlatform{
					waitForOnline: func(sugar *zap.SugaredLogger, ctx context.Context, s *stream.Stream) (*name.WaitForOnlineData, error) {
						<-streamGoneOnlineChan
						return &name.WaitForOnlineData{StreamId: streamGoneOnlineId}, nil
					},
					stream: func(ctx context.Context, s *stream.Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error {
						select {
						case <-ctx.Done():
							return ctx.Err()
						}
					},
				},
			},
			Clock: mClock,
		})
		listener := streamListener.StreamListener{
			Sugar:          sugar,
			DiscordUpdater: &TestDiscordUpdater{},
			SCRedisClient:  scRedisClient,
		}

		s := stream.Stream{
			Id:             "test",
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: time.Time{},
			Listener:       &listener,
			Mutex:          &sc_redis_test.TestMutex{},
			Permanent:      true,
		}

		setupTestStream(scRedisClient, &s)

		broadcaster.HandleStream(&s)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		AssertRedisStreamPersisted(t, scRedisClient, &s)

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusWaiting
		}, s.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		AssertRedisStreamPersisted(t, scRedisClient, &s)

		advance(mClock, time.Minute)

		streamGoneOnlineId = "stream2"
		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return s.Status == stream.StatusGoneLive
		}, 5*time.Second)

		advanceUntilCond(mClock, func() bool {
			return s.SCStreamStarted
		}, 5*time.Second)

		AssertRedisStreamPersisted(t, scRedisClient, &s)
	})
}
