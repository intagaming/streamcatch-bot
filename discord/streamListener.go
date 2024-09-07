package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"sync"
)

type StreamListener struct {
	bot          *Bot
	interaction  *discordgo.Interaction
	message      *discordgo.Message
	messageMutex sync.Mutex
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

	content := fmt.Sprintf("<@%s>", interactionAuthor(sl.interaction).ID) + msg.Content
	if sl.message != nil {
		_, err := sl.bot.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         sl.message.ID,
			Channel:    sl.message.ChannelID,
			Content:    &content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
		if err != nil {
			sl.bot.sugar.Errorw("could not send status to discord", "err", err)
		}
	} else {
		discordMsg, err := sl.bot.session.FollowupMessageCreate(sl.interaction, true, &discordgo.WebhookParams{
			Content:    content,
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

func (sl *StreamListener) Close(s *stream.Stream) {
	sl.UpdateStreamCatchMessage(s)
	if s.Permanent && s.Status != stream.StatusEnded {
		return
	}
	delete(StreamAuthor, s.Id)
	if _, ok := StreamGuild[s.Id]; ok {
		guildId := StreamGuild[s.Id]
		delete(StreamGuild, s.Id)
		delete(GuildStreams[guildId], s.Id)
		if len(GuildStreams[guildId]) == 0 {
			delete(GuildStreams, guildId)
		}
	} else {
		userId := StreamUser[s.Id]
		delete(StreamUser, s.Id)
		delete(UserStreams[userId], s.Id)
		if len(UserStreams[userId]) == 0 {
			delete(UserStreams, userId)
		}
	}
}

func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}
