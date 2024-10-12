package broadcaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/quartz"
	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"streamcatch-bot/broadcaster/bcconfig"
	"streamcatch-bot/broadcaster/platform"
	"streamcatch-bot/broadcaster/platform/name"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamlistener"
	"streamcatch-bot/scredis"
	scredistest "streamcatch-bot/scredis/scredistest"
	"strings"
	"time"
)

var (
	errInvalidUrl = errors.New("invalid stream url")
)

type Config struct {
	TwitchClientId                string
	TwitchClientSecret            string
	TwitchAuthToken               string
	MediaServerRtspHost           string
	MediaServerPublishUser        string
	MediaServerPublishPassword    string
	MediaServerPlaybackUrl        string
	MediaServerApiUrl             string
	FfmpegCmderCreator            func(ctx context.Context, config *Config, streamId stream.Id) FfmpegCmder
	DummyStreamFfmpegCmderCreator func(ctx context.Context, streamUrl string) FfmpegCmder
	StreamAvailableChecker        func(streamId stream.Id) (bool, error)
	Helix                         *helix.Client
	StreamPlatforms               map[name.Name]stream.Platform
	Clock                         quartz.Clock
	StreamerInfoFetcher           func(ctx context.Context, s *stream.Stream) (*stream.Info, error)
	SCRedisClient                 scredis.Client
	DiscordUpdaterCreator         func(s *scredis.RedisStream) (streamlistener.DiscordUpdater, error)
	UseNvidiaGpu                  bool
}

type Broadcaster struct {
	sugar  *zap.SugaredLogger
	agents map[stream.Id]*Agent
	helix  *helix.Client
	Config *Config
}

func (b *Broadcaster) Agents() map[stream.Id]*Agent {
	return b.agents
}

func (b *Broadcaster) MediaServerPublishUser() string {
	return b.Config.MediaServerPublishUser
}

func (b *Broadcaster) MediaServerPublishPassword() string {
	return b.Config.MediaServerPublishPassword
}

func New(sugar *zap.SugaredLogger, cfg *Config) *Broadcaster {
	b := Broadcaster{
		sugar:  sugar,
		agents: make(map[stream.Id]*Agent),
		helix:  cfg.Helix,
		Config: cfg,
	}

	return &b
}

func (b *Broadcaster) MakeLocalStream(ctx context.Context, url string, listener stream.StatusListener, permanent bool) (*stream.Stream, error) {
	id, err := gonanoid.New()
	if err != nil {
		return nil, err
	}
	clock := b.Config.Clock
	mutex := &scredistest.TestMutex{Clock: clock}
	if err := mutex.Lock(); err != nil {
		panic(err)
	}
	s := stream.Stream{
		Id:             stream.Id(id),
		Url:            url,
		Platform:       platform.Local,
		CreatedAt:      clock.Now(),
		ScheduledEndAt: clock.Now().Add(bcconfig.ScheduledEndDuration),
		Listener:       listener,
		Permanent:      permanent,
		Mutex:          mutex,
	}

	info, err := b.Config.StreamerInfoFetcher(ctx, &s)
	if err != nil {
		return nil, err
	}
	s.ThumbnailUrl = info.ThumbnailUrl

	return &s, nil
}

func (b *Broadcaster) MakeStream(ctx context.Context, url string, listener stream.StatusListener, permanent bool) (*stream.Stream, error) {
	checkCmd := exec.CommandContext(ctx, "streamlink", "--can-handle-url", url)
	err := checkCmd.Run()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			if exitError.ExitCode() == 1 {
				return nil, errInvalidUrl
			}
			return nil, exitError
		}
		return nil, err
	}

	var platformName name.Name
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		platformName = platform.YouTube
	} else if strings.Contains(url, "twitch.tv") {
		platformName = platform.Twitch
	} else {
		platformName = platform.Generic
	}

	id, err := gonanoid.New()
	if err != nil {
		return nil, err
	}

	mutex := b.Config.SCRedisClient.StreamMutex(id)
	if err := mutex.Lock(); err != nil {
		panic(err)
	}

	clock := b.Config.Clock
	s := stream.Stream{
		Id:             stream.Id(id),
		Url:            url,
		Platform:       platformName,
		CreatedAt:      clock.Now(),
		ScheduledEndAt: clock.Now().Add(bcconfig.ScheduledEndDuration),
		Listener:       listener,
		Permanent:      permanent,
		Mutex:          mutex,
	}

	info, err := b.Config.StreamerInfoFetcher(ctx, &s)
	if err != nil {
		return nil, err
	}
	s.ThumbnailUrl = info.ThumbnailUrl

	return &s, nil
}

func (b *Broadcaster) HandleStream(s *stream.Stream) *Agent {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, stream.BroadcasterCtxKey{}, b)

	agent := Agent{
		sugar:     b.sugar,
		ctx:       ctx,
		ctxCancel: cancel,
		Stream:    s,
		ffmpegCmder: func(ctx context.Context) FfmpegCmder {
			return b.Config.FfmpegCmderCreator(ctx, b.Config, s.Id)
		},
		dummyStreamFfmpegCmderCreator: func(ctx context.Context) FfmpegCmder {
			return b.Config.DummyStreamFfmpegCmderCreator(ctx, s.Url)
		},
	}

	go agent.Run()

	b.agents[agent.Stream.Id] = &agent

	if !s.Permanent {
		go func() {
			<-ctx.Done()
			delete(b.agents, agent.Stream.Id)
		}()
	}

	return &agent
}

func (b *Broadcaster) RefreshAgent(streamId stream.Id, newScheduledEndAt time.Time) error {
	a, ok := b.agents[streamId]
	if !ok {
		return errors.New(fmt.Sprintf("Agent for streamId %v not found", streamId))
	}
	a.Stream.ScheduledEndAt = newScheduledEndAt
	err := scredis.PersistStream(b.Config.SCRedisClient, a.Stream)
	if err != nil {
		return err
	}
	return nil
}

func (b *Broadcaster) ResumeStream(redisStream *scredis.RedisStream, discordUpdater streamlistener.DiscordUpdater, mutex stream.Mutex) {
	if _, ok := b.agents[stream.Id(redisStream.Id)]; ok {
		return
	}

	sl := streamlistener.StreamListener{
		Sugar:          b.sugar,
		DiscordUpdater: discordUpdater,
		SCRedisClient:  b.Config.SCRedisClient,
	}
	s := stream.Stream{
		Id:             stream.Id(redisStream.Id),
		Url:            redisStream.Url,
		Platform:       redisStream.Platform,
		CreatedAt:      redisStream.CreatedAt,
		ScheduledEndAt: redisStream.ScheduledEndAt,
		LastStatus:     redisStream.LastStatus,
		Status:         redisStream.Status,
		Listener:       &sl,
		ThumbnailUrl:   redisStream.ThumbnailUrl,
		Permanent:      redisStream.Permanent,
		Mutex:          mutex,
		Live:           redisStream.Live,
		LastLiveAt:     redisStream.LastLiveAt,
		Title:          redisStream.Title,
		Author:         redisStream.Author,
	}
	b.HandleStream(&s)
	b.sugar.Infof("Resumed stream %s", s.Id)
}

func (b *Broadcaster) ResumeStreamPoller(ctx context.Context) {
	b.sugar.Info("ResumeStreamPoller starting")
	clock := b.Config.Clock
	go func() {
		t := clock.TickerFunc(ctx, 30*time.Second, func() error {
			b.ResumeStreams()
			return nil
		}, "ResumeStreamPoller")
		err := t.Wait()
		if errors.Is(err, context.Canceled) {
			return
		}
		b.sugar.Errorf("ResumeStreamPoller errored: %v", err)
	}()
}

func (b *Broadcaster) ResumeStreams() {
	scRedisClient := b.Config.SCRedisClient
	ctx := context.Background()
	streams, err := scRedisClient.GetAllStreams(ctx)
	if err != nil {
		panic(err)
	}
	for streamId, streamJson := range streams {
		if _, ok := b.agents[stream.Id(streamId)]; ok {
			continue
		}

		// Obtain right to handle stream
		mutex := scRedisClient.StreamMutex(streamId)
		err := mutex.Lock()
		if err != nil {
			b.sugar.Debugw("Stream locked, not handling", "streamId", streamId)
			continue
		}

		var s scredis.RedisStream
		err = json.Unmarshal([]byte(streamJson), &s)
		if err != nil {
			b.sugar.Errorf("Could not unmarshal stream %s, deleting. Error: %v", streamId, err)
			err := scRedisClient.CleanupStream(ctx, streamId)
			if err != nil {
				panic(err)
			}
			continue
		}

		discordUpdater, err := b.Config.DiscordUpdaterCreator(&s)
		if err != nil {
			b.sugar.Errorf("Could not create discord updater for stream %s, deleting. Error: %v", streamId, err)
			err := scRedisClient.CleanupStream(ctx, streamId)
			if err != nil {
				panic(err)
			}
			continue
		}
		b.ResumeStream(&s, discordUpdater, mutex)
	}
}

func (b *Broadcaster) combineRecordings(s stream.Stream, from time.Time, to time.Time) {
	sugar := b.sugar
	sugar.Infow("combining recordings", "streamId", s.Id, "from", from, "to", to)
	resp, err := http.Get(b.Config.MediaServerPlaybackUrl + "/list?path=" + string(s.Id))
	if err != nil {
		sugar.Errorf("list recordings failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		sugar.Errorf("could not get playback info for stream due to not ok; StatusCode: %d", resp.StatusCode)
		return
	}
	type PlaybackEntry struct {
		Start    string  `json:"start"`
		Duration float64 `json:"duration"`
	}
	var list []PlaybackEntry
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		sugar.Errorf("could not read playback info: %v", err)
		return
	}
	err = json.Unmarshal(bodyBytes, &list)
	if err != nil {
		sugar.Errorf("json unmarshal failed: %v", err)
		return
	}

	var entries []*PlaybackEntry
	for _, entry := range list {
		parsedTime, err := time.Parse("2006-01-02T15:04:05.999999Z", entry.Start)
		if err != nil {
			sugar.Errorf("parse time failed: %v", err)
			return
		}
		if (parsedTime.Equal(from) || parsedTime.After(from)) && (parsedTime.Equal(to) || parsedTime.Before(to)) {
			entries = append(entries, &entry)
		}
	}

	if err := os.MkdirAll("tmp", os.ModePerm); err != nil {
		sugar.Errorf("failed to create tmp folder: %v", err)
		return
	}

	var filenames []string

	for _, entry := range entries {
		filename := filepath.Base(fmt.Sprintf("%s-%s.mp4", s.Id, url.QueryEscape(entry.Start)))
		filenames = append(filenames, filename)
		out, err := os.Create(filepath.Join("tmp", filename))
		if err != nil {
			sugar.Errorf("failed to create file: %v", err)
			return
		}
		fileResp, err := http.Get(fmt.Sprintf(
			"%s/get?path=%s&start=%s&duration=%f&format=mp4",
			b.Config.MediaServerPlaybackUrl,
			s.Id,
			url.QueryEscape(entry.Start),
			entry.Duration))
		if err != nil {
			sugar.Errorf("get recording failed: %v", err)
			return
		}
		_, err = io.Copy(out, fileResp.Body)
		if err != nil {
			sugar.Errorf("download recording failed: %v", err)
			return
		}
		fileResp.Body.Close()
	}

	combinedFileName := fmt.Sprintf("%s-%d.mp4", s.Id, s.LastLiveAt.Unix())
	sugar.Debugf("combined filename: %s", combinedFileName)

	var inputs []string
	filterBuffer := strings.Builder{}
	for i, filename := range filenames {
		inputs = append(inputs, "-i", filepath.Join("tmp", filename))
		filterBuffer.WriteString(fmt.Sprintf("[%d:v:0]scale=854:480,setdar=16/9[%dv];", i, i))
	}
	for i := range filenames {
		filterBuffer.WriteString(fmt.Sprintf("[%dv][%d:a:0]", i, i))
	}
	filterBuffer.WriteString(fmt.Sprintf("concat=n=%d:v=1:a=1[outv][outa]", len(filenames)))

	args := []string{"ffmpeg", "-hide_banner", "-loglevel", "error"}
	args = append(args, inputs...)
	args = append(args, "-filter_complex", filterBuffer.String(),
		"-map", "[outv]", "-map", "[outa]", "-movflags", "+faststart")
	if b.Config.UseNvidiaGpu {
		args = append(args, "-c:v", "h264_nvenc", "-preset", "p1", "-rc:v", "vbr", "-cq:v", "19")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "26")
	}
	args = append(args, filepath.Join("recordings", combinedFileName))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	concatCmd := exec.CommandContext(ctx, args[0], args[1:]...)
	stderr, err := concatCmd.StderrPipe()
	if err != nil {
		sugar.Errorf("failed to establish stderror: %v", err)
		return
	}
	err = concatCmd.Run()
	if err != nil {
		slurp, _ := io.ReadAll(stderr)
		sugar.Errorf("failed to run combine command: %v; stderr: %s", err, slurp)
		return
	}

	var filesToDelete []string
	for _, filename := range filenames {
		filesToDelete = append(filesToDelete, filepath.Join("tmp", filename))
	}
	for _, filename := range filesToDelete {
		err := os.Remove(filename)
		if err != nil {
			sugar.Errorf("could not remove file %s: %v", filename, err)
		}
	}

	sugar.Infow("combine recordings successfully", "streamId", s.Id)
}
