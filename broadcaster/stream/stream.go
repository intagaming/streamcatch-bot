package stream

import (
	"bytes"
	"context"
	"errors"
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
	LastStatus      Status
	Status          Status
	SCStreamStarted bool
	EndedReason     *EndedReason
	EndedError      error
	Listener        StatusListener
	ThumbnailUrl    string
	Permanent       bool
	Mutex           Mutex
	// Used for permanent stream handling. Only catch a stream once.
	Live bool
	// Can be used to detect if the stream ever went online.
	LastLiveAt time.Time
	Title      string
	Author     string
}

func (s *Stream) ChangeStatus(status Status) {
	s.LastStatus = s.Status
	s.Status = status
}

type Mutex interface {
	Lock() error
	Unlock() (bool, error)
	Extend() (bool, error)
	Until() time.Time
}

type Status int

const (
	StatusWaiting Status = iota
	StatusGoneLive
	StatusEnded
)

type EndedReason int

const (
	// ReasonForceStopped ends the whole stream catch, be it a permanent stream catch or not.
	ReasonForceStopped EndedReason = iota
	ReasonTimeout
	ReasonStreamEnded
	ReasonFulfilled
	ReasonErrored
	// ReasonStopOneInstance ends one instance of the permanent stream catch.
	ReasonStopOneInstance
)

type StatusListener interface {
	Status(s *Stream)
	StreamStarted(s *Stream)
	Close(s *Stream)
}

type Info struct {
	ThumbnailUrl string
}

type BroadcasterCtxKey struct{}

var (
	NotOnlineErr = errors.New("stream not online")
)

type Platform interface {
	GetStream(ctx context.Context, stream *Stream) (*name.StreamData, error)
	Stream(ctx context.Context, stream *Stream, pipeWrite *io.PipeWriter, streamlinkErrBuf *bytes.Buffer, ffmpegErrBuf *bytes.Buffer) error
}
