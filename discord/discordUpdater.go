package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"sync"
)

type RealDiscordUpdater struct {
	bot          *Bot
	interaction  *discordgo.Interaction
	message      *discordgo.Message
	messageMutex sync.Mutex
	authorId     string
}

func (r *RealDiscordUpdater) UpdateStreamCatchMessage(s *stream.Stream) {
	r.messageMutex.Lock()
	defer r.messageMutex.Unlock()

	var msg *StreamMessageContent
	switch s.Status {
	case stream.StatusWaiting:
		msg = r.bot.MakeStreamStartedMessage(s)
	case stream.StatusGoneLive:
		msg = r.bot.MakeStreamGoneLiveMessage(s)
	case stream.StatusEnded:
		msg = r.bot.MakeStreamEndedMessage(s)
	default:
		r.bot.sugar.Fatalf("Unknown stream status: %d", s.Status)
		return
	}

	content := fmt.Sprintf("<@%s>", r.authorId) + msg.Content
	if r.message != nil {
		_, err := r.bot.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         r.message.ID,
			Channel:    r.message.ChannelID,
			Content:    &content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
		if err != nil {
			r.bot.sugar.Errorf("could not send status to discord: %v", err)
		}
	} else {
		discordMsg, err := r.bot.session.FollowupMessageCreate(r.interaction, true, &discordgo.WebhookParams{
			Content:    content,
			Components: msg.Components,
			Embeds:     msg.Embeds,
		})
		if err != nil {
			r.bot.sugar.Errorf("could not send status to discord: %v", err)
		}
		r.message = discordMsg
	}
}
