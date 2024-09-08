package discord

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"os"
	"streamcatch-bot/broadcaster"
	"streamcatch-bot/broadcaster/stream"
	"strings"
	"time"
)

var (
	ExtendDuration = 10 * time.Minute
	StreamAuthor   = make(map[stream.Id]*discordgo.User)
	GuildStreams   = make(map[string]map[stream.Id]struct{})
	StreamGuild    = make(map[stream.Id]string)
	UserStreams    = make(map[string]map[stream.Id]struct{})
	StreamUser     = make(map[stream.Id]string)
)

type Bot struct {
	sugar                        *zap.SugaredLogger
	session                      *discordgo.Session
	broadcaster                  *broadcaster.Broadcaster
	mediaServerHlsUrl            string
	mediaServerPlaybackUrl       string
	mediaServerPlaybackUrlPublic string
}

func New(sugar *zap.SugaredLogger, bc *broadcaster.Broadcaster) *Bot {
	botToken := os.Getenv("BOT_TOKEN")
	appId := os.Getenv("APP_ID")

	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		sugar.Panicw("Failed to create new discordgo session", "err", err)
	}

	mediaServerHlsUrl := os.Getenv("MEDIA_SERVER_HLS_URL")
	if mediaServerHlsUrl == "" {
		sugar.Panic("MEDIA_SERVER_HLS_URL is not set")
	}

	mediaServerPlaybackUrl := os.Getenv("MEDIA_SERVER_PLAYBACK_URL")
	if mediaServerPlaybackUrl == "" {
		sugar.Panic("MEDIA_SERVER_PLAYBACK_URL is not set")
	}

	mediaServerPlaybackUrlPublic := os.Getenv("MEDIA_SERVER_PLAYBACK_URL_PUBLIC")
	if mediaServerPlaybackUrlPublic == "" {
		sugar.Panic("MEDIA_SERVER_PLAYBACK_URL_PUBLIC is not set")
	}

	bot := Bot{
		sugar:                        sugar,
		session:                      session,
		broadcaster:                  bc,
		mediaServerHlsUrl:            mediaServerHlsUrl,
		mediaServerPlaybackUrl:       mediaServerPlaybackUrl,
		mediaServerPlaybackUrlPublic: mediaServerPlaybackUrlPublic,
	}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandsHandlers[i.ApplicationCommandData().Name]; ok {
				h(&bot, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			if strings.HasPrefix(customID, "stop_") {
				streamId := stream.Id(strings.TrimPrefix(customID, "stop_"))
				author := interactionAuthor(i.Interaction)
				if !bot.CheckStreamAuthor(i.Interaction, streamId, author) {
					return
				}
				a, ok := bot.broadcaster.Agents()[streamId]
				if !ok {
					bot.sugar.Debugw("Agent not found", "streamId", streamId)
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "The stream is not available anymore.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					if err != nil {
						bot.sugar.Errorw("Failed to respond to interaction", "err", err)
					}
					return
				}

				a.Close(stream.ReasonForceStopped, nil)

				err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredMessageUpdate,
				})
				if err != nil {
					bot.sugar.Errorw("Failed to respond to interaction", "err", err)
				}
				return
			}
			if strings.HasPrefix(customID, "refresh_") {
				streamId := stream.Id(strings.TrimPrefix(customID, "refresh_"))
				author := interactionAuthor(i.Interaction)
				if !bot.CheckStreamAuthor(i.Interaction, streamId, author) {
					return
				}
				a, ok := bot.broadcaster.Agents()[streamId]
				if !ok {
					bot.sugar.Debugw("Agent not found", "streamId", streamId)
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "The stream is not available anymore.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					if err != nil {
						bot.sugar.Errorw("Failed to respond to interaction", "err", err)
					}
					return
				}

				newScheduledEndAt := time.Now().Add(ExtendDuration)
				if a.Stream.ScheduledEndAt.After(newScheduledEndAt) {
					newScheduledEndAt = a.Stream.ScheduledEndAt
				}

				err = bot.broadcaster.RefreshAgent(streamId, newScheduledEndAt)
				if err != nil {
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "An error occurred",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					if err != nil {
						bot.sugar.Errorw("Failed to respond to interaction", "err", err)
					}
					return
				}

				streamStartedMessage := bot.MakeStreamStartedMessage(a.Stream)
				err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: &discordgo.InteractionResponseData{
						Content:    fmt.Sprintf("<@%s>", author.ID) + streamStartedMessage.Content,
						Components: streamStartedMessage.Components,
						Embeds:     streamStartedMessage.Embeds,
					},
				})
				if err != nil {
					bot.sugar.Errorw("could not update interaction", "err", err)
				}
				return
			}
			if strings.HasPrefix(customID, "permanent_recatch_") {
				streamUrl := strings.TrimPrefix(customID, "permanent_recatch_")
				bot.newStreamCatch(i.Interaction, streamUrl, true)

				return
			}
			if strings.HasPrefix(customID, "recatch_") {
				streamUrl := strings.TrimPrefix(customID, "recatch_")
				bot.newStreamCatch(i.Interaction, streamUrl, false)

				return
			}
			//if h, ok := componentsHandlers[i.MessageComponentData().CustomID]; ok {
			//	h(&bot, i)
			//}
		}

	})

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		sugar.Infof("Logged in as %s", r.User.String())
	})

	_, err = session.ApplicationCommandBulkOverwrite(appId, "", commands)
	if err != nil {
		sugar.Fatalf("could not register commands: %s", err)
	}

	err = session.Open()
	if err != nil {
		sugar.Fatalf("could not open session: %s", err)
	}

	return &bot
}

func (bot *Bot) Close() error {
	return bot.session.Close()
}

type optionMap = map[string]*discordgo.ApplicationCommandInteractionDataOption

func parseOptions(options []*discordgo.ApplicationCommandInteractionDataOption) (om optionMap) {
	om = make(optionMap)
	for _, opt := range options {
		om[opt.Name] = opt
	}
	return
}

func (bot *Bot) EditMessage(channelId string, messageId string, message string) {
	_, err := bot.session.ChannelMessageEdit(channelId, messageId, message)

	if err != nil {
		bot.sugar.Panicf("could not edit message: %s", err)
	}
}

func (bot *Bot) newStreamCatch(i *discordgo.Interaction, url string, permanent bool) {
	err := bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		bot.sugar.Errorf("could not respond to interaction: %s", err)
	}

	sl := bot.NewStreamListener(i)
	ctx := context.Background()
	s, err := bot.broadcaster.MakeStream(ctx, url, sl, permanent)
	if err != nil {
		bot.sugar.Errorf("could not create stream: %s", err)
		content := "Failed to create stream."
		_, err := bot.session.InteractionResponseEdit(i, &discordgo.WebhookEdit{
			Content: &content,
		})
		if err != nil {
			bot.sugar.Errorf("could not edit interaction: %s", err)
		}
		return
	}

	author := interactionAuthor(i)
	StreamAuthor[s.Id] = author
	if i.Member != nil {
		if _, ok := GuildStreams[i.GuildID]; !ok {
			GuildStreams[i.GuildID] = make(map[stream.Id]struct{})
		}
		GuildStreams[i.GuildID][s.Id] = struct{}{}
		StreamGuild[s.Id] = i.GuildID
	} else {
		if _, ok := UserStreams[i.User.ID]; !ok {
			UserStreams[i.User.ID] = make(map[stream.Id]struct{})
		}
		UserStreams[i.User.ID][s.Id] = struct{}{}
		StreamGuild[s.Id] = i.GuildID
		StreamUser[s.Id] = i.User.ID
	}

	bot.broadcaster.HandleStream(s)

	msg := bot.MakeRequestReceivedMessage(s)
	_, err = bot.session.InteractionResponseEdit(i, &discordgo.WebhookEdit{
		Content:    &msg.Content,
		Components: &msg.Components,
		Embeds:     &msg.Embeds,
	})
	if err != nil {
		bot.sugar.Errorw("could not respond to interaction", "err", err)
	}
}

func interactionAuthor(i *discordgo.Interaction) *discordgo.User {
	if i.Member != nil {
		return i.Member.User
	}
	return i.User
}

func (bot *Bot) handleStreamCatchCmd(i *discordgo.InteractionCreate, opts optionMap) {
	url := opts["url"].StringValue()
	var permanent bool
	if opt, ok := opts["permanent"]; ok {
		permanent = opt.BoolValue()
	}

	bot.newStreamCatch(i.Interaction, url, permanent)
}

func (bot *Bot) SendUnauthorizedInteractionResponse(i *discordgo.Interaction) {
	err := bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "You can only interact with your own stream catch.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		bot.sugar.Errorw("Failed to respond to interaction", "err", err)
	}
}

const streamcatchCommandName = "streamcatch"
const streamcatchManageCommandName = "scmanage"

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        streamcatchCommandName,
		Description: "Catch a stream the moment it comes online",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Name:        "url",
				Description: "Stream URL",
				Type:        discordgo.ApplicationCommandOptionString,
				Required:    true,
			},
			{
				Name:        "permanent",
				Description: "Whether to catch the stream 24/7 or catch just one.",
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Required:    false,
			},
		},
	},
	{
		Name:        streamcatchManageCommandName,
		Description: "Manage StreamCatch",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Name:        "list-permanent",
				Description: "List all permanent streams scheduled by you.",
				Type:        discordgo.ApplicationCommandOptionSubCommand,
			},
			{
				Name:        "cancel-all-permanent",
				Description: "Cancel all permanent streams scheduled by you.",
				Type:        discordgo.ApplicationCommandOptionSubCommand,
			},
		},
	},
}

var (
	//componentsHandlers = map[string]func(bot *Bot, i *discordgo.InteractionCreate){
	//	"stop": func(bot *Bot, i *discordgo.InteractionCreate) {
	//		a, ok := bot.broadcaster.Agents()[*streamId]
	//		if !ok {
	//			http.Error(w, "agent not found", 404)
	//			return
	//		}
	//	},
	//}
	commandsHandlers = map[string]func(bot *Bot, i *discordgo.InteractionCreate){
		streamcatchCommandName: func(bot *Bot, i *discordgo.InteractionCreate) {
			data := i.ApplicationCommandData()
			bot.handleStreamCatchCmd(i, parseOptions(data.Options))
		},
		streamcatchManageCommandName: func(bot *Bot, i *discordgo.InteractionCreate) {
			bot.handleStreamCatchManageCmd(i)
		},
	}
)

func (bot *Bot) CheckStreamAuthor(i *discordgo.Interaction, streamId stream.Id, author *discordgo.User) bool {
	if StreamAuthor[streamId].ID != author.ID {
		bot.SendUnauthorizedInteractionResponse(i)
		return false
	}
	return true
}

func (bot *Bot) handleStreamCatchManageCmd(i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	switch options[0].Name {
	case "list-permanent":
		var streams map[stream.Id]struct{}
		var ok bool
		if i.Member != nil {
			streams, ok = GuildStreams[i.GuildID]
		} else {
			streams, ok = UserStreams[i.User.ID]
		}
		if !ok || len(streams) == 0 {
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "No permanent streams found.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			if err != nil {
				bot.sugar.Errorw("Failed to respond to interaction", "err", err)
			}
			return
		}

		contentSb := strings.Builder{}
		contentSb.Write([]byte("Here are permanent streams that you scheduled:\n"))
		for streamId := range streams {
			a, ok := bot.broadcaster.Agents()[streamId]
			if !ok {
				bot.sugar.Errorw("Cannot find agent from stream", "streamId", streamId)
				continue
			}
			if !a.Stream.Permanent {
				continue
			}
			contentSb.Write([]byte(fmt.Sprintf("- Stream ID: `%s`; URL: %s\n", streamId, a.Stream.Url)))
		}

		err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: contentSb.String(),
				Flags:   discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsSuppressEmbeds,
			},
		})
		if err != nil {
			bot.sugar.Errorw("Failed to respond to interaction", "err", err)
		}
	case "cancel-all-permanent":
		var streams map[stream.Id]struct{}
		var ok bool
		if i.Member != nil {
			streams, ok = GuildStreams[i.GuildID]
		} else {
			streams, ok = UserStreams[i.User.ID]
		}
		if !ok || len(streams) == 0 {
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "No permanent streams found.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			if err != nil {
				bot.sugar.Errorw("Failed to respond to interaction", "err", err)
			}
			return
		}
		for streamId := range streams {
			a, ok := bot.broadcaster.Agents()[streamId]
			if !ok {
				bot.sugar.Errorw("Cannot find agent from stream", "streamId", streamId)
				continue
			}
			if !a.Stream.Permanent {
				continue
			}
			a.Close(stream.ReasonForceStopped, nil)
		}
		err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "All permanent streams have been stopped.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			bot.sugar.Errorw("Failed to respond to interaction", "err", err)
		}
	}
}
