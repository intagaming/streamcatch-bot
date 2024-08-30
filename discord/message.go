package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster"
	"time"
)

type StreamMessageContent struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
}

func (bot *Bot) MakeStreamEndedMessage(url string, reason broadcaster.EndedReason) *StreamMessageContent {
	var desc string
	switch {
	// TODO: show if the streamer never went online
	case reason == broadcaster.Fulfilled:
		desc = "The stream had ended."
	case reason == broadcaster.Timeout:
		desc = "The stream did not come online in time."
	case reason == broadcaster.ForceStopped:
		desc = "The stream catch has been stopped by the user."
	case reason == broadcaster.Errored:
		desc = "An error has occurred."
	default:
		desc = "The stream catch was stopped for unknown reason."
	}

	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: desc,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Stream URL",
						Value: url,
					},
					{
						Name:  "Status",
						Value: "🔴 Ended",
					},
					// TODO: expiration time
				},
			},
		},
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

func (bot *Bot) MakeStreamStartedMessage(url string, streamId int64, scheduledEndAt time.Time) *StreamMessageContent {
	link := fmt.Sprintf("%s/%d", bot.mediaServerHlsUrl, streamId)
	return &StreamMessageContent{
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
					{
						Name:  "Catch until",
						Value: scheduledEndAt.UTC().Format(time.RFC1123),
					},
				},
			},
		},
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
	}
}

func (bot *Bot) MakeStreamGoneLiveMessage(url string, streamId int64) *StreamMessageContent {
	link := fmt.Sprintf("%s/%d", bot.mediaServerHlsUrl, streamId)
	return &StreamMessageContent{
		Content: fmt.Sprintf("URL: _`%s`_\n**StreamCatch stream is ready!** [Click here to watch.](%s)\nStatus: 🟢 Online", url, link),
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Streamer went online, watch now!",
				URL:         link,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Stream URL",
						Value: url,
					},
					{
						Name:  "Status",
						Value: "🟢 Online",
					},
				},
			},
		},
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

func (bot *Bot) MakeRequestReceivedMessage(url string) *StreamMessageContent {
	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Request to catch is received. Stay tuned for a followup!",
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Stream URL",
						Value: url,
					},
				},
			},
		},
	}
}
