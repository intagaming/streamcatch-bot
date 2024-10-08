package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"
)

type StreamMessageContent struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
}

type PlaybackEntry struct {
	Start    string  `json:"start"`
	Duration float64 `json:"duration"`
}

func (bot *Bot) GetPlaybackURL(s *stream.Stream) (string, error) {
	return fmt.Sprintf("%s/%s-%d.mp4", bot.recordingUrl, s.Id, s.LastLiveAt.Unix()), nil
}

func (bot *Bot) MakeStreamEndedMessage(s *stream.Stream, authorId string) *StreamMessageContent {
	content := fmt.Sprintf("<@%s>", authorId)
	descSb := strings.Builder{}
	switch *s.EndedReason {
	case stream.ReasonStreamEnded:
		descSb.WriteString("The stream had ended.")
	case stream.ReasonTimeout:
		descSb.WriteString("The stream did not come online in time.")
	case stream.ReasonFulfilled:
		descSb.WriteString("Stream was catch successfully. Catch you on the next one!")
	case stream.ReasonForceStopped:
		if !s.Permanent {
			descSb.WriteString("The stream catch has been stopped by the user.")
		} else {
			descSb.WriteString("The permanent stream catch has been stopped by the user.")
		}
	case stream.ReasonStopOneInstance:
		descSb.WriteString("The stream catch has been stopped by the user. Future streams from this streamer will continue to be catch.")
	case stream.ReasonErrored:
		descSb.WriteString("An error has occurred.")
	default:
		descSb.WriteString("The stream catch was stopped for unknown reason.")
	}

	var recordLink string
	var err error
	switch *s.EndedReason {
	case stream.ReasonStreamEnded, stream.ReasonFulfilled, stream.ReasonForceStopped, stream.ReasonStopOneInstance:
		if s.LastLiveAt.IsZero() || s.LastStatus == stream.StatusWaiting {
			break
		}
		recordLink, err = bot.GetPlaybackURL(s)
	default:
	}
	if err != nil {
		bot.sugar.Warnw("Could not get playback URL. This might not be an error.", "StreamId", s.Id, "error", err)
	}

	var components []discordgo.MessageComponent
	if recordLink != "" {
		components = append(components, discordgo.Button{
			Label: "Watch recorded stream",
			Style: discordgo.LinkButton,
			URL:   recordLink,
		})
		descSb.WriteString(fmt.Sprintf("\nYou can watch the recorded stream for the next 24 hours."))
	}
	if !s.Permanent {
		components = append(components, discordgo.Button{
			Label:    "Re-catch",
			Style:    discordgo.SecondaryButton,
			CustomID: fmt.Sprintf("recatch_%s", s.Url),
		})
	} else {
		if *s.EndedReason == stream.ReasonForceStopped {
			components = append(components,
				discordgo.Button{
					Label:    "Re-catch (Permanent)",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("permanent_recatch_%s", s.Url),
				},
			)
		} else {
			components = append(components,
				discordgo.Button{
					Label:    "Cancel all",
					Style:    discordgo.DangerButton,
					CustomID: fmt.Sprintf("force_stop_%s", s.Id),
				},
			)
		}
	}

	var componentsToSend []discordgo.MessageComponent
	if len(components) > 0 {
		componentsToSend = append(componentsToSend, discordgo.ActionsRow{
			Components: components,
		})
	}
	return &StreamMessageContent{
		Content: content,
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: descSb.String(),
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Title",
						Value: s.Title,
					},
					{
						Name:   "Status",
						Value:  "🔴 Ended",
						Inline: true,
					},
					{
						Name:   "Author",
						Value:  s.Author,
						Inline: true,
					},
					{
						Name:   "Stream URL",
						Value:  s.Url,
						Inline: true,
					},
				},
			},
		},
		Components: componentsToSend,
	}
}

func (bot *Bot) MakeStreamStartedMessage(s *stream.Stream, authorId string) *StreamMessageContent {
	content := fmt.Sprintf("<@%s>", authorId)
	link := fmt.Sprintf("%s/%s", bot.mediaServerHlsUrl, s.Id)
	return &StreamMessageContent{
		Content: content,
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: "Ready to catch! Join now.",
				URL:         link,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Status",
						Value: "🟡 Waiting for stream to start",
					},
					{
						Name:   "Stream URL",
						Value:  s.Url,
						Inline: true,
					},
					{
						Name:   "Catch until",
						Value:  s.ScheduledEndAt.UTC().Format(time.RFC1123),
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
						CustomID: fmt.Sprintf("refresh_%s", s.Id),
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

func (bot *Bot) MakeStreamGoneLiveMessage(s *stream.Stream, authorId string) *StreamMessageContent {
	content := fmt.Sprintf("<@%s> %s went live! %s", authorId, s.Author, s.Title)
	link := fmt.Sprintf("%s/%s", bot.mediaServerHlsUrl, s.Id)
	isPermanentStr := "No"
	if s.Permanent {
		isPermanentStr = "Yes"
	}
	return &StreamMessageContent{
		Content: content,
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: fmt.Sprintf("%s went online, watch now!", s.Author),
				URL:         link,
				Thumbnail: &discordgo.MessageEmbedThumbnail{
					URL: s.ThumbnailUrl,
				},
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:  "Title",
						Value: s.Title,
					},
					{
						Name:   "Status",
						Value:  "🟢 Online",
						Inline: true,
					},
					{
						Name:   "Author",
						Value:  s.Author,
						Inline: true,
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
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Cancel",
						Style:    discordgo.DangerButton,
						CustomID: fmt.Sprintf("force_stop_%s", s.Id),
					},
				},
			},
		},
	}
}
