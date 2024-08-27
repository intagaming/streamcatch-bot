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
}

func (sl *StreamListener) Register(streamId int64) {
	streamToStreamListener[streamId] = sl
}

func (sl *StreamListener) Status(stream *broadcaster.Stream, status broadcaster.StreamStatus) {
	var err error
	switch status {
	case broadcaster.StreamStarted:
		msg := sl.bot.MakeStreamStartedMessage(stream.Url, stream.Id)
		_, err = sl.bot.session.InteractionResponseEdit(sl.interaction, &discordgo.WebhookEdit{
			Content:    &msg.Content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
	case broadcaster.GoneLive:
		msg := sl.bot.MakeStreamGoneLiveMessage(stream.Url, stream.Id)
		_, err = sl.bot.session.InteractionResponseEdit(sl.interaction, &discordgo.WebhookEdit{
			Content:    &msg.Content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
	case broadcaster.Ended:
		// Message sent via StreamListener.Close()
		delete(streamToStreamListener, stream.Id)
	case broadcaster.Timeout:
	case broadcaster.ForceStopped:
		// Message sent via StreamListener.Close()
		break
	default:
		_, err = sl.bot.session.ChannelMessageSend(sl.interaction.ChannelID, "Unhandled")
	}
	if err != nil {
		sl.bot.sugar.Errorw("could not send status to discord", "err", err)
	}
}

func (sl *StreamListener) Close(stream *broadcaster.Stream, reason error) {
	msg := sl.bot.MakeStreamEndedMessage(stream.Url, reason)
	_, err := sl.bot.session.ChannelMessageSendComplex(sl.interaction.ChannelID, &discordgo.MessageSend{
		Content:    msg.Content,
		Components: msg.Components,
	})
	if err != nil {
		sl.bot.sugar.Errorw("could not send status to discord", "err", err)
	}
}

func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}
