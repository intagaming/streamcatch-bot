package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"os"
	"strconv"
	"streamcatch-bot/broadcaster"
	"strings"
	"time"
)

var (
	ExtendDuration = 10 * time.Minute
)

type Bot struct {
	sugar             *zap.SugaredLogger
	session           *discordgo.Session
	broadcaster       *broadcaster.Broadcaster
	mediaServerHlsUrl string
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

	bot := Bot{sugar: sugar, session: session, broadcaster: bc, mediaServerHlsUrl: mediaServerHlsUrl}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandsHandlers[i.ApplicationCommandData().Name]; ok {
				h(&bot, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			if strings.HasPrefix(customID, "stop_") {
				streamIdStr := strings.TrimPrefix(customID, "stop_")
				streamId, err := strconv.ParseInt(streamIdStr, 10, 64)
				if err != nil {
					bot.sugar.Debugw("Failed to parse customID", "err", err)
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "An error occurred.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					if err != nil {
						bot.sugar.Errorw("Failed to respond to interaction", "err", err)
					}
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

				a.Close(broadcaster.ForceStopped, nil)

				err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredMessageUpdate,
				})
				if err != nil {
					bot.sugar.Errorw("Failed to respond to interaction", "err", err)
				}
				return
			}
			if strings.HasPrefix(customID, "refresh_") {
				streamIdStr := strings.TrimPrefix(customID, "refresh_")
				streamId, err := strconv.ParseInt(streamIdStr, 10, 64)
				if err != nil {
					bot.sugar.Debugw("Failed to parse customID", "err", err)
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "An error occurred.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					if err != nil {
						bot.sugar.Errorw("Failed to respond to interaction", "err", err)
					}
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
						Content:    streamStartedMessage.Content,
						Components: streamStartedMessage.Components,
						Embeds:     streamStartedMessage.Embeds,
					},
				})
				if err != nil {
					bot.sugar.Errorw("could not update interaction", "err", err)
				}
				return
			}
			if strings.HasPrefix(customID, "recatch_") {
				streamUrl := strings.TrimPrefix(customID, "recatch_")
				bot.newStreamCatch(i.Interaction, streamUrl)

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

func (bot *Bot) newStreamCatch(i *discordgo.Interaction, url string) {
	ctx := context.Background()
	sl := bot.NewStreamListener(i)
	stream, err := bot.broadcaster.MakeStream(ctx, url, sl)
	if err != nil {
		bot.sugar.Errorf("could not create stream: %s", err)
		err := bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to create stream.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			bot.sugar.Panicf("could not respond to interaction: %s", err)
		}
		return
	}
	sl.Register(stream.Id)

	bot.broadcaster.HandleStream(stream)

	msg := bot.MakeRequestReceivedMessage(stream)
	err = bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    msg.Content,
			Components: msg.Components,
			Embeds:     msg.Embeds,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		bot.sugar.Errorw("could not respond to interaction", "err", err)
	}
}

func (bot *Bot) handleStreamCatchCmd(i *discordgo.InteractionCreate, opts optionMap) {
	url := opts["url"].StringValue()

	bot.newStreamCatch(i.Interaction, url)
}

const streamcatchCommandName = "streamcatch"

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
	}
)
