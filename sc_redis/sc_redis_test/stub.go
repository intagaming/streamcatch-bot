package sc_redis

import (
	"context"
	"errors"
	"github.com/coder/quartz"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/sc_redis"
	"sync"
	"time"
)

type TestSCRedisClient struct {
	Clock             quartz.Clock
	Streams           map[string]string
	StreamMutexMap    map[string]stream.Mutex
	StreamAuthorId    map[string]string
	StreamGuildId     map[string]string
	GuildStreams      map[string]map[string]struct{}
	StreamUserId      map[string]string
	UserStreams       map[string]map[string]struct{}
	StreamMessage     map[string]string
	StreamInteraction map[string]string
}

func (t *TestSCRedisClient) GetStreamInteraction(_ context.Context, streamId string) (string, error) {
	return t.StreamInteraction[streamId], nil
}

func (t *TestSCRedisClient) SetStreamInteraction(_ context.Context, streamId string, interactionString string) error {
	t.StreamInteraction[streamId] = interactionString
	return nil
}

func (t *TestSCRedisClient) SetStreamJson(_ context.Context, streamId string, streamJson string) error {
	t.Streams[streamId] = streamJson
	return nil
}

func (t *TestSCRedisClient) GetStreams(context.Context) (map[string]string, error) {
	return t.Streams, nil
}

func (t *TestSCRedisClient) GetStream(_ context.Context, streamId string) (string, error) {
	return t.Streams[streamId], nil
}

func (t *TestSCRedisClient) SetStream(_ context.Context, data *sc_redis.SetStreamData) error {
	t.Streams[data.StreamId] = data.StreamJson
	t.StreamAuthorId[data.StreamId] = data.AuthorId
	if data.GuildId != "" {
		t.StreamGuildId[data.StreamId] = data.GuildId
		if _, ok := t.GuildStreams[data.GuildId]; !ok {
			t.GuildStreams[data.GuildId] = make(map[string]struct{})
		}
		t.GuildStreams[data.GuildId][data.StreamId] = struct{}{}
	} else {
		t.StreamUserId[data.StreamId] = data.AuthorId
		if _, ok := t.UserStreams[data.AuthorId]; !ok {
			t.UserStreams[data.AuthorId] = make(map[string]struct{})
		}
		t.UserStreams[data.AuthorId][data.StreamId] = struct{}{}
	}
	return nil
}

func (t *TestSCRedisClient) CleanupStream(_ context.Context, streamId string) error {
	delete(t.Streams, streamId)
	delete(t.StreamAuthorId, streamId)
	if guildId, ok := t.StreamGuildId[streamId]; ok {
		delete(t.StreamGuildId, streamId)
		delete(t.GuildStreams[guildId], streamId)
	} else if userId, ok := t.StreamUserId[streamId]; ok {
		delete(t.StreamUserId, streamId)
		delete(t.UserStreams[userId], streamId)
	} else {
		panic(errors.New("should not be here"))
	}
	return nil
}

func (t *TestSCRedisClient) StreamMutex(streamId string) stream.Mutex {
	if m, ok := t.StreamMutexMap[streamId]; ok {
		return m
	}
	t.StreamMutexMap[streamId] = &TestMutex{Clock: t.Clock}
	return t.StreamMutexMap[streamId]
}

func (t *TestSCRedisClient) GetStreamMessage(_ context.Context, streamId string) (string, error) {
	return t.StreamMessage[streamId], nil
}

func (t *TestSCRedisClient) SetStreamMessage(_ context.Context, streamId string, messageJson string) error {
	t.StreamMessage[streamId] = messageJson
	return nil
}

func (t *TestSCRedisClient) GetStreamAuthorId(_ context.Context, streamId string) (string, error) {
	return t.StreamAuthorId[streamId], nil
}

func (t *TestSCRedisClient) SetStreamAuthorId(_ context.Context, streamId string, authorId string) error {
	t.StreamAuthorId[streamId] = authorId
	return nil
}

func (t *TestSCRedisClient) GetGuildStreams(_ context.Context, guildId string) ([]string, error) {
	if gs, ok := t.GuildStreams[guildId]; ok {
		streams := make([]string, len(gs))
		i := 0
		for k := range gs {
			streams[i] = k
			i++
		}
		return streams, nil
	}
	return []string{}, nil
}

func (t *TestSCRedisClient) GetUserStreams(_ context.Context, userId string) ([]string, error) {
	if us, ok := t.UserStreams[userId]; ok {
		streams := make([]string, len(us))
		i := 0
		for k := range us {
			streams[i] = k
			i++
		}
		return streams, nil
	}
	return []string{}, nil
}

type TestMutex struct {
	Clock quartz.Clock
	mutex sync.Mutex
	until time.Time
}

func (l *TestMutex) Extend() (bool, error) {
	if l.until.IsZero() {
		return false, errors.New("mutex not locked")
	}
	l.until = l.Clock.Now().Add(8 * time.Second)
	return true, nil
}

func (l *TestMutex) Until() time.Time {
	return l.until
}

func (l *TestMutex) Lock() error {
	if l.mutex.TryLock() {
		l.until = l.Clock.Now().Add(8 * time.Second)
		return nil
	}
	return errors.New("lock failed")
}

func (l *TestMutex) Unlock() (bool, error) {
	l.mutex.Unlock()
	l.until = time.Time{}
	return true, nil
}
