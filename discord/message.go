package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"time"
)

type StreamMessageContent struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
}

func (bot *Bot) MakeStreamEndedMessage(s *stream.Stream) *StreamMessageContent {
	var desc string
	switch *s.EndedReason {
	case stream.ReasonStreamEnded:
		desc = "The stream had ended."
	case stream.ReasonTimeout:
		desc = "The stream did not come online in time."
	case stream.ReasonFulfilled:
		desc = "Stream was catch successfully. Catch you on the next one!"
	case stream.ReasonForceStopped:
		if !s.Permanent {
			desc = "The stream catch has been stopped by the user."
		} else {
			desc = "The permanent stream catch has been stopped by the user."
		}
	case stream.ReasonErrored:
		desc = "An error has occurred."
	default:
		desc = "The stream catch was stopped for unknown reason."
	}

	var components []discordgo.MessageComponent
	if !s.Permanent {
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Re-catch",
						Style:    discordgo.SecondaryButton,
						CustomID: fmt.Sprintf("recatch_%s", s.Url),
					},
				},
			},
		}
	} else {
		if *s.EndedReason == stream.ReasonForceStopped {
			components = []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.Button{
							Label:    "Re-catch (Permanent)",
							Style:    discordgo.SecondaryButton,
							CustomID: fmt.Sprintf("permanent_recatch_%s", s.Url),
						},
					},
				},
			}
		}
	}

	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: desc,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Status",
						Value: "🔴 Ended",
					},
					{
						Name:   "Stream URL",
						Value:  s.Url,
						Inline: true,
					},
				},
			},
		},
		Components: components,
	}
}

func (bot *Bot) MakeStreamStartedMessage(stream *stream.Stream) *StreamMessageContent {
	link := fmt.Sprintf("%s/%s", bot.mediaServerHlsUrl, stream.Id)
	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Ready to catch! Join now.",
				URL:         link,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: stream.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Status",
						Value: "🟡 Waiting for stream to start",
					},
					{
						Name:   "Stream URL",
						Value:  stream.Url,
						Inline: true,
					},
					{
						Name:   "Catch until",
						Value:  stream.ScheduledEndAt.UTC().Format(time.RFC1123),
						Inline: true,
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
						CustomID: fmt.Sprintf("refresh_%s", stream.Id),
					},
					discordgo.Button{
						Label:    "Stop",
						Style:    discordgo.DangerButton,
						CustomID: fmt.Sprintf("stop_%s", stream.Id),
					},
				},
			},
		},
	}
}

func (bot *Bot) MakeStreamGoneLiveMessage(s *stream.Stream) *StreamMessageContent {
	link := fmt.Sprintf("%s/%s", bot.mediaServerHlsUrl, s.Id)
	isPermanentStr := "No"
	if s.Permanent {
		isPermanentStr = "Yes"
	}
	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Streamer went online, watch now!",
				URL:         link,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Status",
						Value: "🟢 Online",
					},
					{
						Name:   "Stream URL",
						Value:  s.Url,
						Inline: true,
					},
					{
						Name:   "Permanent?",
						Value:  isPermanentStr,
						Inline: true,
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
						Label:    "Stop",
						Style:    discordgo.DangerButton,
						CustomID: fmt.Sprintf("stop_%s", s.Id),
					},
				},
			},
		},
	}
}

func (bot *Bot) MakeRequestReceivedMessage(s *stream.Stream) *StreamMessageContent {
	var description string
	if !s.Permanent {
		description = "Request to catch is received. Stay tuned for a followup!"
	} else {
		description = "StreamCatch scheduled. We will notify you when the stream comes online."
	}
	isPermanentStr := "No"
	if s.Permanent {
		isPermanentStr = "Yes"
	}
	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: description,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "Stream URL",
						Value:  s.Url,
						Inline: true,
					},
					{
						Name:   "Permanent?",
						Value:  isPermanentStr,
						Inline: true,
					},
				},
			},
		},
	}
}
