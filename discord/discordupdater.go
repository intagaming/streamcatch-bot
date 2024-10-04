package discord

import (
	"context"
	"encoding/json"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"sync"
)

type RealDiscordUpdater struct {
	Bot          *Bot
	ChannelID    string
	Message      *discordgo.Message
	MessageMutex sync.Mutex
	AuthorId     string
}

func (r *RealDiscordUpdater) ForgetMessage() {
	r.Message = nil
}

func (r *RealDiscordUpdater) UpdateStreamCatchMessage(s *stream.Stream) {
	r.MessageMutex.Lock()
	defer r.MessageMutex.Unlock()

	var msg *StreamMessageContent
	switch s.Status {
	case stream.StatusWaiting:
		if s.EndedReason != nil {
			msg = r.Bot.MakeStreamEndedMessage(s, r.AuthorId)
		} else {
			msg = r.Bot.MakeStreamStartedMessage(s, r.AuthorId)
		}
	case stream.StatusGoneLive:
		msg = r.Bot.MakeStreamGoneLiveMessage(s, r.AuthorId)
	case stream.StatusEnded:
		msg = r.Bot.MakeStreamEndedMessage(s, r.AuthorId)
	default:
		r.Bot.sugar.Fatalf("Unknown stream status: %d", s.Status)
		return
	}

	if r.Message != nil {
		_, err := r.Bot.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         r.Message.ID,
			Channel:    r.Message.ChannelID,
			Content:    &msg.Content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
		if err != nil {
			r.Bot.sugar.Errorf("could not send status to discord: %v", err)
		}
	} else {
		discordMsg, err := r.Bot.session.ChannelMessageSendComplex(r.ChannelID, &discordgo.MessageSend{
			Content:    msg.Content,
			Embeds:     msg.Embeds,
			Components: msg.Components,
		})
		if err != nil {
			r.Bot.sugar.Errorf("could not send status to discord: %v", err)
			return
		}
		r.Message = discordMsg
		msgJson, err := json.Marshal(discordMsg)
		if err != nil {
			panic(err)
		}
		err = r.Bot.scRedisClient.SetStreamMessage(context.Background(), string(s.Id), string(msgJson))
		if err != nil {
			r.Bot.sugar.Errorf("could not set stream message: %v", err)
		}
	}
}
