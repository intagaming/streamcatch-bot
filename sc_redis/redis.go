package sc_redis

import (
	"context"
	"encoding/json"
	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"time"
)

const (
	StreamsKey                = "streams"
	StreamDiscordAuthorIdKey  = "stream_discord_author_id:"
	StreamDiscordMessageIdKey = "stream_discord_message_id:"
	StreamLockKey             = "stream_lock:"
	GuildStreamsKey           = "guild_streams:"
	StreamGuildKey            = "stream_guild:"
	UserStreamsKey            = "user_streams:"
	StreamUserKey             = "stream_user:"
)

type RedisStream struct {
	Id                   string        `json:"id"`
	Url                  string        `json:"url"`
	Platform             name.Name     `json:"platform"`
	CreatedAt            time.Time     `json:"created_at"`
	ScheduledEndAt       time.Time     `json:"scheduled_end_at"`
	Status               stream.Status `json:"status"`
	ThumbnailUrl         string        `json:"thumbnail_url"`
	Permanent            bool          `json:"permanent"`
	PlatformLastStreamId *string       `json:"platform_last_stream_id"`
}

func RedisStreamFromStream(s *stream.Stream) *RedisStream {
	return &RedisStream{
		Id:                   string(s.Id),
		Url:                  s.Url,
		Platform:             s.Platform,
		CreatedAt:            s.CreatedAt,
		ScheduledEndAt:       s.ScheduledEndAt,
		Status:               s.Status,
		ThumbnailUrl:         s.ThumbnailUrl,
		Permanent:            s.Permanent,
		PlatformLastStreamId: s.PlatformLastStreamId,
	}
}

func (redisStream *RedisStream) Marshal() []byte {
	j, err := json.Marshal(redisStream)
	if err != nil {
		panic(err)
	}
	return j
}

func StreamMutex(rs *redsync.Redsync, streamId string) *redsync.Mutex {
	return rs.NewMutex(StreamLockKey + streamId)
}

func CleanupStream(rdb *redis.Client, streamId string) {
	ctx := context.Background()
	_, err := rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, StreamDiscordMessageIdKey+streamId)
		pipe.Del(ctx, StreamDiscordAuthorIdKey+streamId)
		pipe.HDel(ctx, StreamsKey, streamId)
		return nil
	})
	if err != nil {
		panic(err)
	}
}
