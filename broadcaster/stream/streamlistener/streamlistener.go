package streamlistener

import (
	"context"
	"go.uber.org/zap"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/scredis"
)

type StreamListener struct {
	Sugar          *zap.SugaredLogger
	DiscordUpdater DiscordUpdater
	SCRedisClient  scredis.Client
}

func (sl *StreamListener) Status(s *stream.Stream) {
	err := scredis.PersistStream(sl.SCRedisClient, s)
	if err != nil {
		sl.Sugar.Errorf("Error setting stream: %v", err)
	}
	sl.DiscordUpdater.UpdateStreamCatchMessage(s)
}

func (sl *StreamListener) StreamStarted(*stream.Stream) {
}

func (sl *StreamListener) Close(s *stream.Stream) {
	sl.DiscordUpdater.UpdateStreamCatchMessage(s)
	if s.Permanent && s.Status != stream.StatusEnded {
		err := sl.SCRedisClient.DelStreamMessage(context.Background(), string(s.Id))
		if err != nil {
			sl.Sugar.Errorf("Error deleting stream message: %v", err)
		}
		return
	}
	// Stream closed completely. Cleanup.

	_, err := s.Mutex.Unlock()
	if err != nil {
		//sl.bot.sugar.Errorf("could not release stream lock: %v", err)
		panic(err)
	}

	err = sl.SCRedisClient.CleanupStream(context.Background(), string(s.Id))
	if err != nil {
		panic(err)
	}
}

type DiscordUpdater interface {
	UpdateStreamCatchMessage(s *stream.Stream)
}
