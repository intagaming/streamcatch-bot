package scredis

import (
	"context"
	"errors"
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

type Client interface {
	GetStreams(ctx context.Context) (map[string]string, error)
	GetStream(ctx context.Context, streamId string) (string, error)
	SetStream(ctx context.Context, data *SetStreamData) error
	SetStreamJson(ctx context.Context, streamId string, streamJson string) error
	CleanupStream(ctx context.Context, streamId string) error
	StreamMutex(streamId string) stream.Mutex
	GetStreamChannelID(ctx context.Context, streamId string) (string, error)
	SetStreamChannelID(ctx context.Context, streamId string, channelId string) error
	GetStreamMessage(ctx context.Context, streamId string) (string, error)
	SetStreamMessage(ctx context.Context, streamId string, messageJson string) error
	GetStreamAuthorId(ctx context.Context, streamId string) (string, error)
	SetStreamAuthorId(ctx context.Context, streamId string, authorId string) error
	GetGuildStreams(ctx context.Context, guildId string) ([]string, error)
	GetUserStreams(ctx context.Context, userId string) ([]string, error)
}

type RealClient struct {
	Redis   *redis.Client
	Redsync *redsync.Redsync
}

func (r *RealClient) GetStreamChannelID(ctx context.Context, streamId string) (string, error) {
	return r.Redis.Get(ctx, StreamDiscordChannelIDKey+streamId).Result()
}

func (r *RealClient) SetStreamChannelID(ctx context.Context, streamId string, channelId string) error {
	return r.Redis.Set(ctx, StreamDiscordChannelIDKey+streamId, channelId, 0).Err()
}

func (r *RealClient) SetStreamJson(ctx context.Context, streamId string, streamJson string) error {
	return r.Redis.HSet(ctx, StreamsKey, streamId, streamJson).Err()
}

func (r *RealClient) GetStreams(ctx context.Context) (map[string]string, error) {
	return r.Redis.HGetAll(ctx, StreamsKey).Result()
}

func (r *RealClient) GetStream(ctx context.Context, streamId string) (string, error) {
	return r.Redis.HGet(ctx, StreamsKey, streamId).Result()
}

func (r *RealClient) SetStream(ctx context.Context, data *SetStreamData) error {
	_, err := r.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, StreamsKey, data.StreamId, data.StreamJson)
		pipe.Set(ctx, StreamDiscordAuthorIdKey+data.StreamId, data.AuthorId, 0)
		if data.GuildId != "" {
			pipe.SAdd(ctx, GuildStreamsKey+data.GuildId, data.StreamId)
			pipe.Set(ctx, StreamGuildKey+data.StreamId, data.GuildId, 0)
		} else {
			pipe.SAdd(ctx, UserStreamsKey+data.AuthorId, data.StreamId)
			pipe.Set(ctx, StreamUserKey+data.StreamId, data.AuthorId, 0)
		}
		return nil
	})
	return err
}

func (r *RealClient) CleanupStream(ctx context.Context, streamId string) error {
	var streamGuildGetCmd *redis.StringCmd
	var streamUserGetCmd *redis.StringCmd
	_, err := r.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		streamGuildGetCmd = pipe.Get(ctx, StreamGuildKey+streamId)
		streamUserGetCmd = pipe.Get(ctx, StreamUserKey+streamId)
		return nil
	})
	if !errors.Is(err, redis.Nil) {
		return err
	}

	streamGuild := streamGuildGetCmd.Val()
	streamUser := streamUserGetCmd.Val()

	_, err = r.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HDel(ctx, StreamsKey, streamId)
		pipe.Del(ctx, StreamDiscordChannelIDKey+streamId)
		pipe.Del(ctx, StreamDiscordMessageKey+streamId)
		pipe.Del(ctx, StreamDiscordAuthorIdKey+streamId)
		if streamGuild != "" {
			pipe.Del(ctx, StreamGuildKey+streamId)
			pipe.SRem(ctx, GuildStreamsKey+streamGuild, streamId)
		}
		if streamUser != "" {
			pipe.Del(ctx, StreamUserKey+streamId)
			pipe.SRem(ctx, UserStreamsKey+streamUser, streamId)
		}
		return nil
	})
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}

func (r *RealClient) StreamMutex(streamId string) stream.Mutex {
	return r.Redsync.NewMutex(StreamLockKey+streamId, redsync.WithExpiry(MutexDuration), redsync.WithTries(1))
}

func (r *RealClient) GetStreamMessage(ctx context.Context, streamId string) (string, error) {
	return r.Redis.Get(ctx, StreamDiscordMessageKey+streamId).Result()
}

func (r *RealClient) SetStreamMessage(ctx context.Context, streamId string, messageJson string) error {
	return r.Redis.Set(ctx, StreamDiscordMessageKey+streamId, messageJson, 0).Err()
}

func (r *RealClient) GetStreamAuthorId(ctx context.Context, streamId string) (string, error) {
	return r.Redis.Get(ctx, StreamDiscordAuthorIdKey+streamId).Result()
}

func (r *RealClient) SetStreamAuthorId(ctx context.Context, streamId string, authorId string) error {
	return r.Redis.Set(ctx, StreamDiscordAuthorIdKey+streamId, authorId, 0).Err()
}

func (r *RealClient) GetGuildStreams(ctx context.Context, guildId string) ([]string, error) {
	return r.Redis.SMembers(ctx, GuildStreamsKey+guildId).Result()
}

func (r *RealClient) GetUserStreams(ctx context.Context, userId string) ([]string, error) {
	return r.Redis.SMembers(ctx, UserStreamsKey+userId).Result()
}

func PersistStream(scRedisClient Client, s *stream.Stream) error {
	return scRedisClient.SetStreamJson(context.Background(), string(s.Id), string(RedisStreamFromStream(s).Marshal()))
}
