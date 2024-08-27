package broadcaster

import (
	"go.uber.org/zap/zaptest"
	"testing"
	"time"
)

type testListener struct{}

func (tl *testListener) Status(stream *Stream, status StreamStatus) {}
func (tl *testListener) Close(stream *Stream, reason error)         {}

func TestAgent_DummyStream(t *testing.T) {
	logger := zaptest.NewLogger(t)
	broadcaster := New(logger.Sugar(), &Config{})
	listener := testListener{}

	_ = broadcaster.HandleStream(&Stream{
		Id:             1,
		Url:            "http://TEST_URL",
		Platform:       "twitch",
		CreatedAt:      time.Now(),
		ScheduledEndAt: time.Now().Add(ScheduledEndDuration),
		Listener:       &listener,
	})

	// TODO: expect stream to start
}
