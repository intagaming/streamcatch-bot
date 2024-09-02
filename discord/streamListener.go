package discord

import (
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster"
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

func (sl *StreamListener) UpdateStreamCatchMessage(stream *broadcaster.Stream) {
	sl.messageMutex.Lock()
	defer sl.messageMutex.Unlock()

	var msg *StreamMessageContent
	switch stream.Status {
	case broadcaster.Waiting:
		msg = sl.bot.MakeStreamStartedMessage(stream)
	case broadcaster.GoneLive:
		msg = sl.bot.MakeStreamGoneLiveMessage(stream)
	case broadcaster.Ended:
		msg = sl.bot.MakeStreamEndedMessage(stream)
	default:
		sl.bot.sugar.Fatalf("Unknown stream status: %d", stream.Status)
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

func (sl *StreamListener) Status(stream *broadcaster.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (sl *StreamListener) StreamStarted(stream *broadcaster.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (sl *StreamListener) Close(stream *broadcaster.Stream) {
	sl.UpdateStreamCatchMessage(stream)
}

func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}
