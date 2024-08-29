package discord

import (
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster"
)

var (
	streamToStreamListener = make(map[int64]*StreamListener)
)

type StreamListener struct {
	bot         *Bot
	interaction *discordgo.Interaction
	message     *discordgo.Message
}

func (sl *StreamListener) Register(streamId int64) {
	streamToStreamListener[streamId] = sl
}

func (sl *StreamListener) Status(stream *broadcaster.Stream, status broadcaster.StreamStatus) {
	switch status {
	case broadcaster.StreamStarted:
		streamStartedMessage := sl.bot.MakeStreamStartedMessage(stream.Url, stream.Id, stream.ScheduledEndAt)
		msg, err := sl.bot.session.FollowupMessageCreate(sl.interaction, true, &discordgo.WebhookParams{
			Content:    streamStartedMessage.Content,
			Components: streamStartedMessage.Components,
			Embeds:     streamStartedMessage.Embeds,
		})
		if err != nil {
			sl.bot.sugar.Errorw("could not send status to discord", "err", err)
		}
		sl.message = msg
	case broadcaster.GoneLive:
		// TODO: wait for StreamStarted message to be sent
		msg := sl.bot.MakeStreamGoneLiveMessage(stream.Url, stream.Id)
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
	case broadcaster.Ended:
		// Message sent via StreamListener.Close()
		delete(streamToStreamListener, stream.Id)
	case broadcaster.Timeout:
	case broadcaster.ForceStopped:
		// Message sent via StreamListener.Close()
		break
	default:
		sl.bot.sugar.Debugw("unhandled status", "status", status)
	}
}

func (sl *StreamListener) Close(stream *broadcaster.Stream, reason error) {
	sl.bot.sugar.Infof("Close!!")
	msg := sl.bot.MakeStreamEndedMessage(stream.Url, reason)
	if sl.message == nil {
		return
	}
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
}

func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}
