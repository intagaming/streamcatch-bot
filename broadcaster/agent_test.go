package broadcaster

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
	"io"
	"sync"
	"testing"
	"time"
)

type testListener struct {
	lastStatus  StreamStatus
	closeCalled bool
	closeReason error
}

func (tl *testListener) Status(stream *Stream, status StreamStatus) {
	tl.lastStatus = status
}
func (tl *testListener) Close(stream *Stream, reason error) {
	tl.closeCalled = true
	tl.closeReason = reason
}

type testFfmpegCmder struct {
	ctx     context.Context
	stdin   io.Reader
	started bool
}

func (t *testFfmpegCmder) SetStdin(pipe io.Reader) {
	t.stdin = pipe
}

func (t *testFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testFfmpegCmder) Start() error {
	t.started = true
	return nil
}

func (t *testFfmpegCmder) Wait() error {
	<-t.ctx.Done()
	return nil
}

type testDummyStreamFfmpegCmder struct {
	ctx     context.Context
	stdout  io.Writer
	started bool
}

func (t *testDummyStreamFfmpegCmder) SetStdout(pipe io.Writer) {
	t.stdout = pipe
}

func (t *testDummyStreamFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testDummyStreamFfmpegCmder) Start() error {
	t.started = true
	return nil
}

func (t *testDummyStreamFfmpegCmder) Wait() error {
	<-t.ctx.Done()
	return nil
}

func TestAgent(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("FfmpegStreamHappyCase", func(t *testing.T) {
		ffmpegCmder := &testFfmpegCmder{}
		var dummyFfmpegCmder *testDummyStreamFfmpegCmder
		streamAvailableChan := make(chan struct{})
		streamAvailableCalledTime := 0
		streamGoneOnlineChan := make(chan struct{})
		streamerData := []byte{69, 110, 105, 99, 101}
		streamEndChan := make(chan struct{})

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
				select {
				case <-ffmpegCmder.ctx.Done():
					return errors.New("unexpected ffmpegCmder exit")
				case <-streamGoneOnlineChan:
					return nil
				}
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				_, err := pipeWrite.Write(streamerData)
				if err != nil {
					return fmt.Errorf("unexpected streamer write error: %w", err)
				}
				select {
				case <-ctx.Done():
					return errors.New("unexpected streamer context end; should not end")
				case <-streamEndChan:
				}
				// block
				return nil
			},
		})
		listener := testListener{}

		a := broadcaster.HandleStream(&Stream{
			Id:             1,
			Url:            "http://TEST_URL",
			Platform:       "twitch",
			CreatedAt:      time.Now(),
			ScheduledEndAt: time.Now().Add(ScheduledEndDuration),
			Listener:       &listener,
		})

		assert.Eventually(t, func() bool {
			return dummyFfmpegCmder.started && ffmpegCmder.started
		}, time.Second, 10*time.Millisecond)

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

		// expect StreamCatch stream available
		// TODO: skip time instead of waiting around
		assert.Eventually(t, func() bool {
			// Need 2 times to guarantee stream is not started, because streamAvailableChecker is called before setting
			// StreamStarted
			return streamAvailableCalledTime >= 2
		}, 20*time.Second, 10*time.Millisecond)
		assert.False(t, a.StreamStarted())

		streamAvailableChan <- struct{}{}

		assert.Eventually(t, func() bool {
			return a.StreamStarted()
		}, 5*time.Second, 10*time.Millisecond)

		// expect StreamCatch gone online ok
		assert.False(t, a.GoneOnline())

		streamGoneOnlineChan <- struct{}{}

		assert.Eventually(t, func() bool {
			return a.GoneOnline()
		}, 5*time.Second, 10*time.Millisecond)

		// expect ffmpegCmder to receive stream data from Streamer
		ffmpegCmderIn = make([]byte, len(streamerData))
		_, err = ffmpegCmder.stdin.Read(ffmpegCmderIn)
		if err != nil {
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", err)
		}
		assert.Equal(t, streamerData, ffmpegCmderIn)

		// expect stream to end
		// TODO: listener instead
		assert.False(t, listener.closeCalled)
		// TODO: wait 30s; skip time instead
		time.Sleep(32 * time.Second)
		streamEndChan <- struct{}{}

		assert.Eventually(t, func() bool {
			return listener.closeCalled
		}, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, listener.closeReason, ReasonNormal)
	})
}
