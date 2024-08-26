package discord

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"os"
	"strconv"
	"streamcatch-bot/broadcaster"
	"strings"
)

type Bot struct {
	sugar       *zap.SugaredLogger
	session     *discordgo.Session
	broadcaster *broadcaster.Broadcaster
}

func New(sugar *zap.SugaredLogger) *Bot {
	botToken := os.Getenv("BOT_TOKEN")
	appId := os.Getenv("APP_ID")

	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		sugar.Panicw("Failed to create new discordgo session", "err", err)
	}

	bc := broadcaster.New(sugar)

	bot := Bot{sugar: sugar, session: session, broadcaster: bc}

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
					return
				}

				a, ok := bot.broadcaster.Agents()[streamId]
				if !ok {
					bot.sugar.Debugw("Agent not found", "streamId", streamId)
					return
				}

				a.Close(broadcaster.ReasonForceStopped(), "")
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

func (bot *Bot) respondInteractionMessage(i *discordgo.Interaction, message string) {
	err := bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
		},
	})

	if err != nil {
		bot.sugar.Panicf("could not respond to interaction: %s", err)
	}
}

func (bot *Bot) EditMessage(channelId string, messageId string, message string) {
	_, err := bot.session.ChannelMessageEdit(channelId, messageId, message)

	if err != nil {
		bot.sugar.Panicf("could not edit message: %s", err)
	}
}

type StreamListener struct {
	bot         *Bot
	interaction *discordgo.Interaction
}

func (sl StreamListener) Status(stream *broadcaster.Stream, status broadcaster.StreamStatus) {
	var err error
	switch status {
	case broadcaster.StreamStarted:
		content := "Stream started"
		_, err = sl.bot.session.InteractionResponseEdit(sl.interaction, &discordgo.WebhookEdit{
			Content: &content,
			Components: &[]discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.Button{
							Label:    "Stop",
							Style:    discordgo.DangerButton,
							CustomID: fmt.Sprintf("stop_%d", stream.Id),
						},
					},
				},
			},
		})
	case broadcaster.GoneLive:
		_, err = sl.bot.session.ChannelMessageSend(sl.interaction.ChannelID, "Stream gone live")
	case broadcaster.Ended:
		_, err = sl.bot.session.ChannelMessageSend(sl.interaction.ChannelID, "Stream ended")
	case broadcaster.Timeout:
		_, err = sl.bot.session.ChannelMessageSend(sl.interaction.ChannelID, "Stream timeout")
	case broadcaster.ForceStopped:
		break
	default:
		_, err = sl.bot.session.ChannelMessageSend(sl.interaction.ChannelID, "Unhandled")
	}
	if err != nil {
		sl.bot.sugar.Errorf("could not send status to discord: %w", err)
	}
}
func (bot *Bot) NewStreamListener(i *discordgo.Interaction) *StreamListener {
	return &StreamListener{bot: bot, interaction: i}
}

func (bot *Bot) handleStreamCatch(i *discordgo.InteractionCreate, opts optionMap) {
	url := opts["url"].StringValue()

	// TODO: real interrupt context
	ctx := context.Background()
	stream, err := broadcaster.MakeStream(ctx, url, bot.NewStreamListener(i.Interaction))
	if err != nil {
		bot.sugar.Errorf("could not create stream: %s", err)
		bot.respondInteractionMessage(i.Interaction, "Failed to create stream.")
		return
	}

	bot.broadcaster.HandleStream(stream)

	bot.respondInteractionMessage(i.Interaction, "Please wait...")
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
			bot.handleStreamCatch(i, parseOptions(data.Options))
		},
	}
)
