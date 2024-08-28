package broadcaster

import (
	"context"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
	"io"
	"testing"
	"time"
)

type testListener struct{}

func (tl *testListener) Status(stream *Stream, status StreamStatus) {}
func (tl *testListener) Close(stream *Stream, reason error)         {}

type testFfmpegCmder struct {
	stdin io.Reader
}

func (t *testFfmpegCmder) SetStdin(pipe io.Reader) {
	t.stdin = pipe
}

func (t *testFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testFfmpegCmder) Start() error {
	return nil
}

func (t *testFfmpegCmder) Wait() error {
	return nil
}

type testDummyStreamFfmpegCmder struct {
	stdout io.Writer
}

func (t *testDummyStreamFfmpegCmder) SetStdout(pipe io.Writer) {
	t.stdout = pipe
}

func (t *testDummyStreamFfmpegCmder) SetStderr(pipe io.Writer) {
}

func (t *testDummyStreamFfmpegCmder) Start() error {
	return nil
}

func (t *testDummyStreamFfmpegCmder) Wait() error {
	// TODO: wait for context
}

func TestAgent_DummyStream(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ffmpegCmder := &testFfmpegCmder{}
	dummyFfmpegCmder := &testDummyStreamFfmpegCmder{}
	broadcaster := New(logger.Sugar(), &Config{
		FfmpegCmderCreator: func(ctx context.Context, config *Config, streamId int64) FfmpegCmder {
			return ffmpegCmder
		},
		DummyStreamFfmpegCmderCreator: func(ctx context.Context, streamUrl string) DummyStreamFfmpegCmder {
			return dummyFfmpegCmder
		},
		StreamAvailableChecker: func(streamId int64) (bool, error) {
			return false, nil
		},
		StreamWaiter: func(agent *Agent) error {
			return nil
		},
		Streamer: func(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter) error {
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
		return a.streamStarted
	}, time.Second, 10*time.Millisecond)
	//
	//// TODO: expect ffmpeg stream to receive dummy stream outputs
	//dummyOutData := []byte{1, 2, 3, 4, 5}
	//_, err := dummyFfmpegCmder.stdout.Write(dummyOutData)
	//if err != nil {
	//	t.Fatalf("Failed to write dummy data: %v", err)
	//}
	//ffmpegCmderIn := make([]byte, 5)
	//_, err = ffmpegCmder.stdin.Read(ffmpegCmderIn)
	//if err != nil {
	//	t.Fatalf("Failed to read stdin ffmpeg cmder: %v", err)
	//}
	//
	//assert.Equal(t, dummyOutData, ffmpegCmderIn)
}
