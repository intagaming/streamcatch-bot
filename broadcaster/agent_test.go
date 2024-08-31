package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
	"io"
	"sync"
	"testing"
	"time"
)

type testListener struct {
	status        StreamStatus
	closeReason   *EndedReason
	closeError    error
	streamStarted bool
}

func (tl *testListener) Status(stream *Stream) {
	tl.status = stream.Status
}
func (tl *testListener) Close(stream *Stream) {
	tl.closeReason = stream.EndedReason
	tl.closeError = stream.EndedError
}
func (tl *testListener) StreamStarted(stream *Stream) {
	tl.streamStarted = true
}

type testFfmpegCmder struct {
	ctx   context.Context
	stdin io.Reader
}

func (t *testFfmpegCmder) SetStdin(pipe io.Reader) {
	t.stdin = pipe
}

func (t *testFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testFfmpegCmder) Start() error {
	return t.ctx.Err()
}

func (t *testFfmpegCmder) Wait() error {
	<-t.ctx.Done()
	return t.ctx.Err()
}

type testDummyStreamFfmpegCmder struct {
	ctx             context.Context
	stdout          io.Writer
	waitToStart     bool
	waitToStartChan chan struct{}
}

func (t *testDummyStreamFfmpegCmder) SetStdout(pipe io.Writer) {
	t.stdout = pipe
}

func (t *testDummyStreamFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testDummyStreamFfmpegCmder) Start() error {
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

func TestAgent(t *testing.T) {
	logger := zaptest.NewLogger(t)

	advanceUntilCond := func(mClock *quartz.Mock, cond func() bool, desired time.Duration) {
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
			// Give time for agent's goroutine to catch the Ticker's channel
			<-time.After(10 * time.Millisecond)
			if cond() {
				return
			}
		}
		assert.True(t, cond())
	}

	t.Run("HappyCase", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamAvailableCalledTime := 0
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerData := []byte{69, 110, 105, 99, 101}
		streamEndChan := make(chan struct{}, 1)
		streamerRetryUntilSuccess := 2

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				streamAvailableCalledTime += 1
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamWaiter: func(agent *Agent) error {
				<-streamGoneOnlineChan
				return nil
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
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
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		broadcaster.HandleStream(&stream)

		// expect StreamCatch stream available
		assert.False(t, listener.streamStarted)

		streamAvailableChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.streamStarted
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
		assert.NotEqual(t, listener.status, GoneLive)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.status == GoneLive
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
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", err)
		}

		assert.Equal(t, streamerData, ffmpegCmderIn)

		assert.NotEqual(t, listener.status, Ended)

		advanceUntilCond(mClock, func() bool {
			return listener.status == Ended
		}, stream.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, listener.closeReason)
		assert.Equal(t, Fulfilled, *listener.closeReason)
	})

	t.Run("StreamNeverCameOnline", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				return true, nil
			},
			StreamWaiter: func(agent *Agent) error {
				select {}
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				return errors.New("should not be called")
			},
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		broadcaster.HandleStream(&stream)

		advanceUntilCond(mClock, func() bool {
			return listener.streamStarted
		}, time.Second)

		advanceUntilCond(mClock, func() bool {
			return listener.status == Ended
		}, stream.ScheduledEndAt.Sub(mClock.Now())+5*time.Second)

		assert.NotNil(t, listener.closeReason)
		assert.Equal(t, Timeout, *listener.closeReason)
	})

	t.Run("StreamOnlineThenOfflineImmediatelyThenNeverCameBackOn", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerEndChan := make(chan struct{}, 1)
		streamerCalledTime := 0

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				return true, nil
			},
			StreamWaiter: func(agent *Agent) error {
				<-streamGoneOnlineChan
				return nil
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				streamerCalledTime += 1
				if streamerCalledTime <= 1 {
					<-streamerEndChan
				}
				return nil
			},
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		broadcaster.HandleStream(&stream)

		advanceUntilCond(mClock, func() bool {
			return listener.streamStarted
		}, time.Second)

		assert.Equal(t, Waiting, listener.status)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.status == GoneLive
		}, time.Second)

		streamerEndChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return streamerCalledTime >= MaxRetries+1
		}, 3*time.Minute)

		advanceUntilCond(mClock, func() bool {
			return listener.status == Ended
		}, time.Second)

		assert.NotNil(t, listener.closeReason)
		assert.Equal(t, Errored, *listener.closeReason)
		assert.Equal(t, FailedToStreamError, listener.closeError)
	})

	t.Run("StreamOnlineThenOfflineThenCameBackOn", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamGoneOnlineChan := make(chan struct{}, 1)
		streamerEndChan := make(chan struct{}, 1)
		streamerCalledTime := 0

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				return true, nil
			},
			StreamWaiter: func(agent *Agent) error {
				<-streamGoneOnlineChan
				return nil
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				streamerCalledTime += 1
				<-streamerEndChan
				return nil
			},
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		broadcaster.HandleStream(&stream)

		advanceUntilCond(mClock, func() bool {
			return listener.streamStarted
		}, time.Second)

		assert.Equal(t, Waiting, listener.status)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.status == GoneLive
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
			return listener.status == Ended
		}, stream.ScheduledEndAt.Sub(mClock.Now())+time.Minute)

		assert.NotNil(t, listener.closeReason)
		assert.Equal(t, Fulfilled, *listener.closeReason)
	})

	t.Run("StreamCameOnlineBeforeStreamCatchReady", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx: ctx,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamWaiter: func(agent *Agent) error {
				<-streamGoneOnlineChan
				return nil
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				return errors.New("should not go here")
			},
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		broadcaster.HandleStream(&stream)

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.status == GoneLive
		}, 5*time.Second)

		assert.False(t, listener.streamStarted)

		streamAvailableChan <- struct{}{}

		advanceUntilCond(mClock, func() bool {
			return listener.streamStarted
		}, 5*time.Second)

		// We're happy if there's no runtime error so far.
	})

	t.Run("StreamCatchStoppedBeforeDummyStreamStarts", func(t *testing.T) {
		mClock := quartz.NewMock(t)
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{}, 1)
		streamGoneOnlineChan := make(chan struct{}, 1)

		broadcaster := New(logger.Sugar(), &Config{
			FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
				ffmpegCmder.ctx = ctx
				return ffmpegCmder
			},
			DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
				dummyFfmpegCmder = &testDummyStreamFfmpegCmder{
					ctx:         ctx,
					waitToStart: true,
				}
				return dummyFfmpegCmder
			},
			StreamAvailableChecker: func(streamId int64) (bool, error) {
				select {
				case <-streamAvailableChan:
					return true, nil
				default:
					return false, nil
				}
			},
			StreamWaiter: func(agent *Agent) error {
				<-streamGoneOnlineChan
				return nil
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				return errors.New("should not go here")
			},
			Clock: mClock,
		})
		listener := testListener{}

		stream := Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      mClock.Now(),
			ScheduledEndAt: mClock.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		}
		agent := broadcaster.HandleStream(&stream)

		agent.Close(ForceStopped, nil)

		// We're happy if there's no runtime error so far.
	})
}
