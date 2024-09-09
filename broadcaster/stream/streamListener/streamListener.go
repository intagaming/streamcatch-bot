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

func (sl *StreamListener) PersistStream(s *stream.Stream) {
	err := sl.SCRedisClient.SetStreamJson(context.Background(), string(s.Id), string(sc_redis.RedisStreamFromStream(s).Marshal()))
	if err != nil {
		sl.Sugar.Errorf("Error setting stream: %v", err)
	}
}

func (sl *StreamListener) Status(s *stream.Stream) {
	sl.PersistStream(s)
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
