package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"io"
	"net/http"
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
	resp, err := http.Get(bot.mediaServerPlaybackUrl + "/list?path=" + string(s.Id))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(fmt.Sprintf("could not get playback info for stream due to not ok; StatusCode: %d", resp.StatusCode))
	}
	var list []PlaybackEntry
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("could not read playback info: %w", err)
	}
	err = json.Unmarshal(bodyBytes, &list)
	if err != nil {
		return "", err
	}

	if len(list) == 0 {
		return "", errors.New("list empty")
	}
	lastEntry := list[len(list)-1]
	return fmt.Sprintf("%s/get?path=%s&start=%s&duration=%f&format=mp4", bot.mediaServerPlaybackUrlPublic, s.Id, lastEntry.Start, lastEntry.Duration), nil
}

func (bot *Bot) MakeStreamEndedMessage(s *stream.Stream) *StreamMessageContent {
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
	case stream.ReasonErrored:
		descSb.WriteString("An error has occurred.")
	default:
		descSb.WriteString("The stream catch was stopped for unknown reason.")
	}

	var recordLink string
	var err error
	switch *s.EndedReason {
	case stream.ReasonStreamEnded:
	case stream.ReasonFulfilled:
	case stream.ReasonForceStopped:
		if s.PlatformLastStreamId == nil {
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
		}
	}

	return &StreamMessageContent{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "StreamCatch",
				Description: descSb.String(),
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
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: components,
			},
		},
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
