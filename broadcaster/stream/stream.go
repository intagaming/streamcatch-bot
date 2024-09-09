package stream

import (
	"bytes"
	"context"
	"go.uber.org/zap"
	"io"
	"streamcatch-bot/broadcaster/platform/name"
	"time"
)

type Id string

type Stream struct {
	Id              Id
	Url             string
	Platform        name.Name
	CreatedAt       time.Time
	ScheduledEndAt  time.Time
	Status          Status
	SCStreamStarted bool
	EndedReason     *EndedReason
	EndedError      error
	Listener        StatusListener
	ThumbnailUrl    string
	Permanent       bool
	// Used for permanent stream handling. Only catch a stream once.
	// Can be used to detect if the stream ever went online.
	PlatformLastStreamId *string
	Mutex                Mutex
}

type Mutex interface {
	Lock() error
	Unlock() (bool, error)
}

type Status int

const (
	StatusWaiting Status = iota
	StatusGoneLive
	StatusEnded
)

type EndedReason int

const (
	ReasonForceStopped EndedReason = iota
	ReasonTimeout
	ReasonStreamEnded
	ReasonFulfilled
	ReasonErrored
)

type StatusListener interface {
	Status(stream *Stream)
	StreamStarted(stream *Stream)
	Close(stream *Stream)
}

type Info struct {
	ThumbnailUrl string
}

type BroadcasterCtxKey struct{}

type Platform interface {
	WaitForOnline(sugar *zap.SugaredLogger, ctx context.Context, stream *Stream) (*name.WaitForOnlineData, error)
	Stream(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error
}
