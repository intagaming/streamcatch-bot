package broadcaster

import (
	"bytes"
	"context"
	"go.uber.org/zap"
	"io"
	"streamcatch-bot/broadcaster/platform"
	"time"
)

type Stream struct {
	Id             int64
	Url            string
	Platform       platform.Name
	CreatedAt      time.Time
	ScheduledEndAt time.Time
	TerminatedAt   time.Time
	Status         StreamStatus
	EndedReason    *EndedReason
	EndedError     error
	Listener       StreamStatusListener
	ThumbnailUrl   string
}

type StreamStatus int

const (
	Waiting StreamStatus = iota
	GoneLive
	Ended
)

type EndedReason int

const (
	ForceStopped EndedReason = iota
	Timeout
	StreamEnded
	Fulfilled
	Errored
)

type StreamStatusListener interface {
	Status(stream *Stream)
	StreamStarted(stream *Stream)
	Close(stream *Stream)
}

type StreamInfo struct {
	ThumbnailUrl string
}

type broadcasterCtxKey struct{}

type StreamPlatform interface {
	WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *Stream) error
	Stream(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error
}
