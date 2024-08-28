package broadcaster

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
	"io"
	"sync"
	"testing"
	"time"
)

type testListener struct{}

func (tl *testListener) Status(stream *Stream, status StreamStatus) {}
func (tl *testListener) Close(stream *Stream, reason error)         {}

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

	t.Run("FfmpegStreamReceivesDummyStream", func(t *testing.T) {
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
				return false, nil
			},
			StreamWaiter: func(agent *Agent) error {
				// Stream will never come online
				<-ffmpegCmder.ctx.Done()
				return errors.New("stream went online when it should not")
			},
			Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
				return nil
			},
		})
		listener := testListener{}

		_ = broadcaster.HandleStream(&Stream{
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
		var writeErr error
		go func() {
			_, err := dummyFfmpegCmder.stdout.Write(dummyOutData)
			writeErr = err
			dummyWriteWg.Done()
		}()

		ffmpegCmderIn := make([]byte, 5)
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
		if writeErr != nil {
			t.Errorf("Failed to write dummy data: %v", err)
		}

		assert.Equal(t, dummyOutData, ffmpegCmderIn)
	})
}
