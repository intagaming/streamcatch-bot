package sc_redis

import (
	"context"
	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
	"streamcatch-bot/broadcaster/stream"
)

type SetStreamData struct {
	StreamId   string
	StreamJson string
	AuthorId   string
	GuildId    string
}

type SCRedisClient interface {
	GetStreams(ctx context.Context) (map[string]string, error)
	GetStream(ctx context.Context, streamId string) (string, error)
	SetStream(ctx context.Context, data *SetStreamData) error
	SetStreamJson(ctx context.Context, streamId string, streamJson string) error
	CleanupStream(ctx context.Context, streamId string) error
	StreamMutex(streamId string) stream.Mutex
	GetStreamMessage(ctx context.Context, streamId string) (string, error)
	SetStreamMessage(ctx context.Context, streamId string, messageJson string) error
	GetStreamAuthorId(ctx context.Context, streamId string) (string, error)
	SetStreamAuthorId(ctx context.Context, streamId string, authorId string) error
	GetGuildStreams(ctx context.Context, guildId string) ([]string, error)
	GetUserStreams(ctx context.Context, userId string) ([]string, error)
}

type RealSCRedisClient struct {
	Redis   *redis.Client
	Redsync *redsync.Redsync
}

func (r *RealSCRedisClient) SetStreamJson(ctx context.Context, streamId string, streamJson string) error {
	return r.Redis.HSet(ctx, StreamsKey, streamId, streamJson).Err()
}

func (r *RealSCRedisClient) GetStreams(ctx context.Context) (map[string]string, error) {
	return r.Redis.HGetAll(ctx, StreamsKey).Result()
}

func (r *RealSCRedisClient) GetStream(ctx context.Context, streamId string) (string, error) {
	return r.Redis.HGet(ctx, StreamsKey, streamId).Result()
}

func (r *RealSCRedisClient) SetStream(ctx context.Context, data *SetStreamData) error {
	_, err := r.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, StreamsKey, data.StreamId, data.StreamJson)
		pipe.Set(ctx, StreamDiscordAuthorIdKey+data.StreamId, data.AuthorId, 0)
		//pipe.SAdd(ctx, Guil)
	})
	//_, err = bot.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
	//	pipe.HSet(ctx, sc_redis.StreamsKey, sc_redis.RedisStreamFromStream(s).Marshal(), 0)
	//	pipe.Set(ctx, sc_redis.StreamDiscordAuthorIdKey+string(s.Id), author.ID, 0)
	//	if i.Member != nil {
	//		pipe.SAdd(ctx, sc_redis.GuildStreamsKey+i.GuildID, s.Id)
	//		pipe.Set(ctx, sc_redis.StreamGuildKey+string(s.Id), i.GuildID, 0)
	//	} else {
	//		pipe.SAdd(ctx, sc_redis.UserStreamsKey+i.User.ID, s.Id)
	//		pipe.Set(ctx, sc_redis.StreamUserKey+string(s.Id), i.User.ID, 0)
	//	}
	//	return nil
	//})
	panic("implement me")
}

func (r *RealSCRedisClient) CleanupStream(ctx context.Context, streamId string) error {
	_, err := r.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		streamGuild := pipe.Get(ctx, StreamGuildKey+streamId).Val()
		streamUser := pipe.Get(ctx, StreamUserKey+streamId).Val()

		pipe.HDel(ctx, StreamsKey, streamId)
		pipe.Del(ctx, StreamDiscordMessageKey+streamId)
		pipe.Del(ctx, StreamDiscordAuthorIdKey+streamId)
		pipe.SRem(ctx, GuildStreamsKey+streamGuild, streamId)
		pipe.SRem(ctx, UserStreamsKey+streamUser, streamId)
		return nil
	})
	return err
}

func (r *RealSCRedisClient) StreamMutex(streamId string) stream.Mutex {
	return r.Redsync.NewMutex(StreamLockKey+streamId, redsync.WithExpiry(MutexDuration))
}

func (r *RealSCRedisClient) GetStreamMessage(ctx context.Context, streamId string) (string, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RealSCRedisClient) SetStreamMessage(ctx context.Context, streamId string, messageJson string) error {
	//TODO implement me
	panic("implement me")
}

func (r *RealSCRedisClient) GetStreamAuthorId(ctx context.Context, streamId string) (string, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RealSCRedisClient) SetStreamAuthorId(ctx context.Context, streamId string, authorId string) error {
	//TODO implement me
	panic("implement me")
}

func (r *RealSCRedisClient) GetGuildStreams(ctx context.Context, guildId string) ([]string, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RealSCRedisClient) GetUserStreams(ctx context.Context, userId string) ([]string, error) {
	//TODO implement me
	panic("implement me")
}

func PersistStream(scRedisClient SCRedisClient, s *stream.Stream) error {
	return scRedisClient.SetStreamJson(context.Background(), string(s.Id), string(RedisStreamFromStream(s).Marshal()))
}
