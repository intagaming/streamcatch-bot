package discord

import (
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster"
	"strings"
)

type StreamMessageContent struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
}

func (bot *Bot) MakeStreamEndedMessage(url string, reason error) *StreamMessageContent {
	var contentBuilder strings.Builder
	contentBuilder.WriteString(fmt.Sprintf("URL: **`%s`**\n", url))
	switch {
	// TODO: show if the streamer never went online
	case errors.Is(reason, broadcaster.ReasonNormal):
	case errors.Is(reason, broadcaster.ReasonTimeout):
		contentBuilder.WriteString("The stream had ended.")
	case errors.Is(reason, broadcaster.ReasonForceStopped):
		contentBuilder.WriteString("The stream catch has been stopped.")
	case errors.Is(reason, broadcaster.ReasonErrored):
		contentBuilder.WriteString("An error has occurred.")
	default:
		contentBuilder.WriteString("The stream catch was stopped for unknown reason.")
	}

	return &StreamMessageContent{
		Content: contentBuilder.String(),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Re-catch",
						Style:    discordgo.SecondaryButton,
						CustomID: fmt.Sprintf("recatch_%s", url),
					},
				},
			},
		},
	}
}

func (bot *Bot) MakeStreamStartedMessage(url string, streamId int64) *StreamMessageContent {
	link := fmt.Sprintf("%s/%d", bot.mediaServerHlsUrl, streamId)
	return &StreamMessageContent{
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label: "Watch",
						Style: discordgo.LinkButton,
						URL:   link,
					},
					discordgo.Button{
						Label:    "Refresh",
						Style:    discordgo.SecondaryButton,
						CustomID: fmt.Sprintf("refresh_%d", streamId),
					},
					discordgo.Button{
						Label:    "Stop",
						Style:    discordgo.DangerButton,
						CustomID: fmt.Sprintf("stop_%d", streamId),
					},
				},
			},
		},
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Ready to catch! Join now.",
				URL:         link,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Stream URL",
						Value: url,
					},
					{
						Name:  "Status",
						Value: "🟡 Waiting for stream to start",
					},
					// TODO: expiration time
				},
			},
		},
	}
}

func (bot *Bot) MakeStreamGoneLiveMessage(url string, streamId int64) *StreamMessageContent {
	link := fmt.Sprintf("%s/%d", bot.mediaServerHlsUrl, streamId)
	return &StreamMessageContent{
		Content: fmt.Sprintf("URL: _`%s`_\n**StreamCatch stream is ready!** [Click here to watch.](%s)\nStatus: 🟢 Online", url, link),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Stop",
						Style:    discordgo.DangerButton,
						CustomID: fmt.Sprintf("stop_%d", streamId),
					},
				},
			},
		},
	}
}
