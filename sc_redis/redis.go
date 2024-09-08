package sc_redis

import (
	"encoding/json"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"time"
)

const (
	StreamsKey               = "streams"
	StreamDiscordAuthorIdKey = "stream_discord_author_id:"
	StreamDiscordMessageKey  = "stream_discord_message:"
	StreamLockKey            = "stream_lock:"
	GuildStreamsKey          = "guild_streams:"
	StreamGuildKey           = "stream_guild:"
	UserStreamsKey           = "user_streams:"
	StreamUserKey            = "stream_user:"
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
