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
	statusHistory []StreamStatus
	closeCalled   bool
	closeReason   error
}

func (tl *testListener) Status(stream *Stream, status StreamStatus) {
	tl.statusHistory = append(tl.statusHistory, status)
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
	mClock := quartz.NewMock(t)

	t.Run("FfmpegStreamHappyCase", func(t *testing.T) {
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
				select {
				case <-ffmpegCmder.ctx.Done():
					return errors.New("unexpected ffmpegCmder exit")
				case <-streamGoneOnlineChan:
					return nil
				}
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
					return errors.New("unexpected streamer context end; should not end")
				case <-streamEndChan:
				}
				return nil
			},
			Clock: mClock,
		})
		listener := testListener{}
		waitForAndPopStreamStatus := func(status StreamStatus) {
			assert.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.NotZero(t, listener.statusHistory)
				assert.Equal(t, status, listener.statusHistory[0])
			}, time.Second, 10*time.Millisecond)
			listener.statusHistory = listener.statusHistory[1:]
		}

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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Need to expect StreamAvailableChecker to be called at least 2 times to guarantee stream is not started,
		// because streamAvailableChecker is called before setting StreamStarted
		// TODO: advance until streamAvailableCalledTime satisfy
		advanceUntilCond := func(cond func() bool, desired time.Duration) {
			for {
				p, ok := mClock.Peek()
				if !ok || p > desired {
					mClock.Advance(desired).MustWait(ctx)
					select {
					case <-time.After(10 * time.Millisecond):
					}
					break
				}
				mClock.Advance(p).MustWait(ctx)
				desired -= p
				// Give time for agent's goroutine to catch the Ticker's channel
				select {
				case <-time.After(10 * time.Millisecond):
				}
				if cond() {
					break
				}
			}
		}
		advanceUntilCond(func() bool {
			return streamAvailableCalledTime >= 2
		}, 20*time.Second)
		assert.GreaterOrEqual(t, streamAvailableCalledTime, 2)

		assert.False(t, a.StreamStarted())

		streamAvailableChan <- struct{}{}

		advanceUntilCond(func() bool {
			return a.StreamStarted()
		}, 5*time.Second)
		assert.True(t, a.StreamStarted())

		waitForAndPopStreamStatus(StreamStarted)

		// expect StreamCatch gone online ok
		assert.False(t, a.GoneOnline())

		streamGoneOnlineChan <- struct{}{}

		advanceUntilCond(func() bool {
			return a.GoneOnline()
		}, 5*time.Second)
		assert.True(t, a.GoneOnline())

		waitForAndPopStreamStatus(GoneLive)

		// expect ffmpegCmder to receive stream data from Streamer
		var streamReadErr error
		ffmpegCmderIn = make([]byte, len(streamerData))
		isRead := false
		go func() {
			_, streamReadErr = ffmpegCmder.stdin.Read(ffmpegCmderIn)
			isRead = true
		}()
		advanceUntilCond(func() bool {
			return isRead
		}, 30*time.Second)

		if streamReadErr != nil {
			t.Fatalf("Failed to read stdin ffmpeg cmder: %v", err)
		}

		assert.Equal(t, streamerData, ffmpegCmderIn)

		// expect stream to end
		assert.False(t, listener.closeCalled)

		// Skip time for the stream catch to be considered successful
		desired := DurationToConsiderCatchedOk + time.Second
		for {
			p, ok := mClock.Peek()
			if !ok || p > desired {
				mClock.Advance(desired).MustWait(ctx)
				break
			}
			mClock.Advance(p).MustWait(ctx)
			desired -= p
		}

		streamEndChan <- struct{}{}

		advanceUntilCond(func() bool {
			return listener.closeCalled
		}, 5*time.Second)
		assert.True(t, listener.closeCalled)
		assert.Equal(t, listener.closeReason, ReasonNormal)
		waitForAndPopStreamStatus(Ended)
	})
}
