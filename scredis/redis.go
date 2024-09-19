package scredis

import (
	"encoding/json"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"time"
)

const (
	StreamsKey                = "streams"
	StreamDiscordAuthorIdKey  = "stream_discord_author_id:"
	StreamDiscordChannelIDKey = "stream_discord_channel_id:"
	StreamDiscordMessageKey   = "stream_discord_message:"
	StreamLockKey             = "stream_lock:"
	GuildStreamsKey           = "guild_streams:"
	StreamGuildKey            = "stream_guild:"
	UserStreamsKey            = "user_streams:"
	StreamUserKey             = "stream_user:"
	MutexDuration             = 15 * time.Second
)

type RedisStream struct {
	Id             string        `json:"id"`
	Url            string        `json:"url"`
	Platform       name.Name     `json:"platform"`
	CreatedAt      time.Time     `json:"created_at"`
	ScheduledEndAt time.Time     `json:"scheduled_end_at"`
	LastStatus     stream.Status `json:"last_status"`
	Status         stream.Status `json:"status"`
	ThumbnailUrl   string        `json:"thumbnail_url"`
	Permanent      bool          `json:"permanent"`
	Live           bool          `json:"live"`
	LastLiveAt     time.Time     `json:"last_live_at"`
}

func RedisStreamFromStream(s *stream.Stream) *RedisStream {
	return &RedisStream{
		Id:             string(s.Id),
		Url:            s.Url,
		Platform:       s.Platform,
		CreatedAt:      s.CreatedAt,
		ScheduledEndAt: s.ScheduledEndAt,
		LastStatus:     s.LastStatus,
		Status:         s.Status,
		ThumbnailUrl:   s.ThumbnailUrl,
		Permanent:      s.Permanent,
		Live:           s.Live,
	}
}

func (redisStream *RedisStream) Marshal() []byte {
	j, err := json.Marshal(redisStream)
	if err != nil {
		panic(err)
	}
	return j
}
