package streamListener

import (
	"context"
	"go.uber.org/zap"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/sc_redis"
)

type StreamListener struct {
	Sugar          *zap.SugaredLogger
	DiscordUpdater DiscordUpdater
	SCRedisClient  sc_redis.SCRedisClient
}

func (sl *StreamListener) Status(s *stream.Stream) {
	err := sc_redis.PersistStream(sl.SCRedisClient, s)
	if err != nil {
		sl.Sugar.Errorf("Error setting stream: %v", err)
	}
	sl.DiscordUpdater.UpdateStreamCatchMessage(s)
}

func (sl *StreamListener) StreamStarted(s *stream.Stream) {
	sl.DiscordUpdater.UpdateStreamCatchMessage(s)
}

func (sl *StreamListener) Close(s *stream.Stream) {
	sl.DiscordUpdater.UpdateStreamCatchMessage(s)
	if s.Permanent && s.Status != stream.StatusEnded {
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
