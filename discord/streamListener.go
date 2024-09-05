package discord

import (
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"sync"
)

var (
	streamToStreamListener = make(map[int64]*StreamListener)
)

type StreamListener struct {
	bot          *Bot
	interaction  *discordgo.Interaction
	message      *discordgo.Message
	messageMutex sync.Mutex
}

func (sl *StreamListener) Register(streamId int64) {
	streamToStreamListener[streamId] = sl
}

func (sl *StreamListener) UpdateStreamCatchMessage(s *stream.Stream) {
	sl.messageMutex.Lock()
	defer sl.messageMutex.Unlock()

	var msg *StreamMessageContent
	switch s.Status {
	case stream.StatusWaiting:
		msg = sl.bot.MakeStreamStartedMessage(s)
	case stream.StatusGoneLive:
		msg = sl.bot.MakeStreamGoneLiveMessage(s)
	case stream.StatusEnded:
		msg = sl.bot.MakeStreamEndedMessage(s)
	default:
		sl.bot.sugar.Fatalf("Unknown stream status: %d", s.Status)
		return
	}

	if sl.message != nil {
		_, err := sl.bot.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         sl.message.ID,
			Channel:    sl.message.ChannelID,
			Content:    &msg.Content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
		if err != nil {
			sl.bot.sugar.Errorw("could not send status to discord", "err", err)
		}
	} else {
		discordMsg, err := sl.bot.session.FollowupMessageCreate(sl.interaction, true, &discordgo.WebhookParams{
			Content:    msg.Content,
			Components: msg.Components,
			Embeds:     msg.Embeds,
		})
		if err != nil {
			sl.bot.sugar.Errorw("could not send status to discord", "err", err)
		}
		sl.message = discordMsg
	}
}

func (sl *StreamListener) Status(stream *stream.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (sl *StreamListener) StreamStarted(stream *stream.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (sl *StreamListener) Close(stream *stream.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}
