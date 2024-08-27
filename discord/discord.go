package discord

import (
	"context"
	"errors"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"net/http"
	"os"
	"strconv"
	"streamcatch-bot/broadcaster"
	"strings"
)

type Bot struct {
	sugar             *zap.SugaredLogger
	session           *discordgo.Session
	broadcaster       *broadcaster.Broadcaster
	mediaServerHlsUrl string
}

func New(sugar *zap.SugaredLogger) *Bot {
	botToken := os.Getenv("BOT_TOKEN")
	appId := os.Getenv("APP_ID")

	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		sugar.Panicw("Failed to create new discordgo session", "err", err)
	}

	var twitchClientId = os.Getenv("TWITCH_CLIENT_ID")
	if twitchClientId == "" {
		sugar.Panic("TWITCH_CLIENT_ID is not set")
	}
	var twitchClientSecret = os.Getenv("TWITCH_CLIENT_SECRET")
	if twitchClientSecret == "" {
		sugar.Panic("TWITCH_CLIENT_SECRET is not set")
	}
	mediaServerRtspHost := os.Getenv("MEDIA_SERVER_RTSP_HOST")
	if mediaServerRtspHost == "" {
		sugar.Panic("MEDIA_SERVER_RTSP_HOST is not set")
	}
	mediaServerPublishUser := os.Getenv("MEDIA_SERVER_PUBLISH_USER")
	if mediaServerPublishUser == "" {
		sugar.Panic("MEDIA_SERVER_PUBLISH_USER is not set")
	}
	mediaServerPublishPassword := os.Getenv("MEDIA_SERVER_PUBLISH_PASSWORD")
	if mediaServerPublishPassword == "" {
		sugar.Panic("MEDIA_SERVER_PUBLISH_PASSWORD is not set")
	}
	mediaServerApiUrl := os.Getenv("MEDIA_SERVER_API_URL")
	if mediaServerApiUrl == "" {
		sugar.Panic("MEDIA_SERVER_API_URL is not set")
	}
	var twitchAuthToken = os.Getenv("TWITCH_AUTH_TOKEN")
	if twitchAuthToken == "" {
		sugar.Warn("TWITCH_AUTH_TOKEN is not set")
	}

	bc := broadcaster.New(sugar, &broadcaster.Config{
		TwitchClientId:                twitchClientId,
		TwitchClientSecret:            twitchClientSecret,
		TwitchAuthToken:               twitchAuthToken,
		MediaServerRtspHost:           mediaServerRtspHost,
		MediaServerPublishUser:        mediaServerPublishUser,
		MediaServerPublishPassword:    mediaServerPublishPassword,
		MediaServerApiUrl:             mediaServerApiUrl,
		FfmpegCmderCreator:            broadcaster.NewRealFfmpegCmder,
		DummyStreamFfmpegCmderCreator: broadcaster.NewRealDummyStreamFfmpegCmder,
		StreamAvailableChecker: func(streamId int64) (bool, error) {
			resp, err := http.Get(mediaServerApiUrl + "/v3/paths/get/" + strconv.FormatInt(streamId, 10))
			if err != nil {
				return false, err
			}
			if resp.StatusCode != http.StatusOK {
				return false, errors.New("response was not 200 but " + resp.Status)
			}
			return true, nil
		},
	})

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

				a.Close(broadcaster.ReasonForceStopped, "")

				err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredMessageUpdate,
				})
				if err != nil {
					bot.sugar.Errorw("Failed to respond to interaction", "err", err)
				}
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

func (bot *Bot) handleStreamCatch(i *discordgo.InteractionCreate, opts optionMap) {
	url := opts["url"].StringValue()

	// TODO: real interrupt context
	ctx := context.Background()
	sl := bot.NewStreamListener(i.Interaction)
	stream, err := broadcaster.MakeStream(ctx, url, sl)
	if err != nil {
		bot.sugar.Errorf("could not create stream: %s", err)
		err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to create stream.",
			},
		})
		if err != nil {
			bot.sugar.Panicf("could not respond to interaction: %s", err)
		}
		return
	}
	sl.Register(stream.Id)

	bot.broadcaster.HandleStream(stream)

	msg := bot.MakeRequestReceivedMessage(url)
	err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    msg.Content,
			Components: msg.Components,
			Embeds:     msg.Embeds,
		},
	})
	if err != nil {
		bot.sugar.Errorw("could not respond to interaction", "err", err)
	}
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
