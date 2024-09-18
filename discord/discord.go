package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"os"
	"streamcatch-bot/broadcaster"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamlistener"
	"streamcatch-bot/scredis"
	"strings"
	"time"
)

var (
	ExtendDuration = 10 * time.Minute
)

type Bot struct {
	initializationCtx            context.Context
	cancelInitializationCtx      context.CancelFunc
	scRedisClient                scredis.Client
	sugar                        *zap.SugaredLogger
	session                      *discordgo.Session
	broadcaster                  *broadcaster.Broadcaster
	mediaServerHlsUrl            string
	mediaServerPlaybackUrl       string
	mediaServerPlaybackUrlPublic string
}

func New(sugar *zap.SugaredLogger, bc *broadcaster.Broadcaster, scRedisClient scredis.Client) *Bot {
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

	initializationCtx, cancelInitializationCtx := context.WithTimeout(context.Background(), 30*time.Second)
	bot := Bot{
		initializationCtx:            initializationCtx,
		cancelInitializationCtx:      cancelInitializationCtx,
		sugar:                        sugar,
		session:                      session,
		broadcaster:                  bc,
		mediaServerHlsUrl:            mediaServerHlsUrl,
		mediaServerPlaybackUrl:       mediaServerPlaybackUrl,
		mediaServerPlaybackUrlPublic: mediaServerPlaybackUrlPublic,
		scRedisClient:                scRedisClient,
	}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})
			if err != nil {
				bot.sugar.Errorf("could not respond to interaction: %s", err)
			}

			if h, ok := commandsHandlers[i.ApplicationCommandData().Name]; ok {
				h(&bot, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			for component, handleFn := range componentHandlers {
				if strings.HasPrefix(customID, component.Prefix) {
					err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: component.DeferType,
					})
					if err != nil {
						bot.sugar.Errorf("could not respond to interaction: %s", err)
					}

					arg := strings.TrimPrefix(customID, component.Prefix)
					handleFn(&bot, i, arg)
					return
				}
			}
		}
	})

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		sugar.Infof("Discord Bot logged in as %s", r.User.String())
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

func (bot *Bot) Init() {
	bot.cancelInitializationCtx()
}

func (bot *Bot) Close() error {
	return bot.session.Close()
}

func (bot *Bot) EditMessage(channelId string, messageId string, message string) {
	_, err := bot.session.ChannelMessageEdit(channelId, messageId, message)

	if err != nil {
		bot.sugar.Panicf("could not edit message: %s", err)
	}
}

func (bot *Bot) newStreamCatch(i *discordgo.Interaction, url string, permanent bool) {
	sl := streamlistener.StreamListener{
		Sugar: bot.sugar,
		DiscordUpdater: &RealDiscordUpdater{
			Bot:       bot,
			ChannelID: i.ChannelID,
			Message:   nil,
			AuthorId:  interactionAuthor(i).ID,
		},
		SCRedisClient: bot.scRedisClient,
	}
	ctx := context.Background()
	s, err := bot.broadcaster.MakeStream(ctx, url, &sl, permanent)
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
	err = bot.scRedisClient.SetStream(ctx, &scredis.SetStreamData{
		StreamId:   string(s.Id),
		StreamJson: string(scredis.RedisStreamFromStream(s).Marshal()),
		AuthorId:   author.ID,
		GuildId:    i.GuildID,
	})
	if err != nil {
		// TODO: handle err
		panic(err)
	}
	err = bot.scRedisClient.SetStreamChannelID(ctx, string(s.Id), i.ChannelID)
	if err != nil {
		// TODO: handle err
		panic(err)
	}

	bot.broadcaster.HandleStream(s)

	msg := bot.MakeRequestReceivedMessage(s)
	_, err = bot.session.InteractionResponseEdit(i, &discordgo.WebhookEdit{
		Content:    &msg.Content,
		Components: &msg.Components,
		Embeds:     &msg.Embeds,
	})
	if err != nil {
		bot.sugar.Errorf("could not respond to interaction: %v", err)
	}
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
		},
	})
	if err != nil {
		bot.sugar.Errorf("Failed to respond to interaction: %v", err)
	}
}

func (bot *Bot) CheckStreamAuthor(i *discordgo.Interaction, streamId stream.Id, author *discordgo.User) bool {
	streamAuthorId, err := bot.scRedisClient.GetStreamAuthorId(context.Background(), string(streamId))
	if errors.Is(err, redis.Nil) {
		return false
	}
	if err != nil {
		bot.sugar.Errorf("could not get stream author id for stream %s: %v", streamId, err)
		return false
	}
	if streamAuthorId != author.ID {
		bot.SendUnauthorizedInteractionResponse(i)
		return false
	}
	return true
}

func (bot *Bot) handleStreamCatchManageCmd(i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	switch options[0].Name {
	case "list-permanent":
		var streamIds []string
		var err error
		ctx := context.Background()
		if i.Member != nil {
			streamIds, err = bot.scRedisClient.GetGuildStreams(ctx, i.GuildID)
		} else {
			streamIds, err = bot.scRedisClient.GetUserStreams(ctx, i.User.ID)
		}
		if err != nil {
			bot.sugar.Errorf("could not get stream list: %v", err)
			content := "An error occurred."
			_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			if err != nil {
				bot.sugar.Errorf("Failed to edit interaction: %v", err)
			}
			return
		}
		if len(streamIds) == 0 {
			content := "No permanent streams found."
			_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			if err != nil {
				bot.sugar.Errorf("Failed to edit interaction: %v", err)
			}
			return
		}

		streams, err := bot.scRedisClient.GetStreams(ctx, streamIds)
		if err != nil {
			bot.sugar.Errorf("could not get streams: %v", err)
			content := "An error occurred."
			_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			if err != nil {
				bot.sugar.Errorf("Failed to edit interaction: %v", err)
			}
			return
		}

		contentSb := strings.Builder{}
		contentSb.Write([]byte("Here are the permanent streams that you've scheduled:\n"))
		for _, streamId := range streamIds {
			streamJson, ok := streams[streamId]
			if !ok {
				panic(fmt.Sprintf("could not find stream %s", streamId))
			}

			var redisStream scredis.RedisStream
			err = json.Unmarshal([]byte(streamJson), &redisStream)
			if err != nil {
				bot.sugar.Errorf("Could not parse redis stream %s: %v", streamId, err)
				continue
			}
			if !redisStream.Permanent {
				continue
			}
			contentSb.Write([]byte(fmt.Sprintf("- Stream ID: `%s`; URL: %s\n", streamId, redisStream.Url)))
		}

		content := contentSb.String()
		_, err = bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		if err != nil {
			bot.sugar.Errorf("Failed to edit interaction: %v", err)
		}
	case "cancel-all-permanent":
		var streams []string
		ctx := context.Background()
		var err error
		if i.Member != nil {
			streams, err = bot.scRedisClient.GetGuildStreams(ctx, i.GuildID)
		} else {
			streams, err = bot.scRedisClient.GetUserStreams(ctx, i.User.ID)
		}
		if err != nil {
			bot.sugar.Errorf("could not get stream list: %v", err)
			content := "An error occurred."
			_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			if err != nil {
				bot.sugar.Errorf("Failed to respond to interaction: %v", err)
			}
			return
		}
		if len(streams) == 0 {
			content := "No permanent streams found."
			_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			if err != nil {
				bot.sugar.Errorf("Failed to respond to interaction: %v", err)
			}
			return
		}

		for _, streamId := range streams {
			a, ok := bot.broadcaster.Agents()[stream.Id(streamId)]
			if !ok {
				bot.sugar.Errorf("Cannot find agent from stream %s", streamId)
				continue
			}
			if !a.Stream.Permanent {
				continue
			}
			a.Close(stream.ReasonForceStopped, nil)
		}
		content := "All permanent streams have been stopped."
		_, err = bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		if err != nil {
			bot.sugar.Errorf("Failed to respond to interaction: %v", err)
		}
	}
}

func (bot *Bot) StopStream(i *discordgo.InteractionCreate, streamId stream.Id, getReason func(s *stream.Stream) stream.EndedReason) {
	author := interactionAuthor(i.Interaction)
	if !bot.CheckStreamAuthor(i.Interaction, streamId, author) {
		_, err := bot.session.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You don't have permission to do this.",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			bot.sugar.Errorf("Failed to respond to interaction: %v", err)
		}
		return
	}

	a, ok := bot.broadcaster.Agents()[streamId]
	if !ok {
		bot.sugar.Debugw("Agent not found", "streamId", streamId)
		_, err := bot.session.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "The stream is not available anymore.",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			bot.sugar.Errorf("Failed to respond to interaction: %v", err)
		}
		return
	}

	closed := a.Close(getReason(a.Stream), nil)
	var err error
	if closed {
		msg := bot.MakeStreamEndedMessage(a.Stream)
		_, err = bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    &msg.Content,
			Components: &msg.Components,
			Embeds:     &msg.Embeds,
		})
	} else {
		_, err = bot.session.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Could not stop the stream.",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
	}
	if err != nil {
		bot.sugar.Errorf("Failed to respond to interaction: %v", err)
	}
}

func interactionAuthor(i *discordgo.Interaction) *discordgo.User {
	if i.Member != nil {
		return i.Member.User
	}
	return i.User
}

type optionMap = map[string]*discordgo.ApplicationCommandInteractionDataOption

func parseOptions(options []*discordgo.ApplicationCommandInteractionDataOption) (om optionMap) {
	om = make(optionMap)
	for _, opt := range options {
		om[opt.Name] = opt
	}
	return
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

type ComponentHandler struct {
	Prefix    string
	DeferType discordgo.InteractionResponseType
}

var (
	StopComponentHandler = ComponentHandler{
		Prefix:    "stop_",
		DeferType: discordgo.InteractionResponseDeferredMessageUpdate,
	}
	ForceStopComponentHandler = ComponentHandler{
		Prefix:    "force_stop_",
		DeferType: discordgo.InteractionResponseDeferredMessageUpdate,
	}
	RefreshComponentHandler = ComponentHandler{
		Prefix:    "refresh_",
		DeferType: discordgo.InteractionResponseDeferredMessageUpdate,
	}
	PermanentRecatchComponentHandler = ComponentHandler{
		Prefix:    "permanent_recatch_",
		DeferType: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}
	RecatchComponentHandler = ComponentHandler{
		Prefix:    "recatch_",
		DeferType: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}
)

var (
	commandsHandlers = map[string]func(bot *Bot, i *discordgo.InteractionCreate){
		streamcatchCommandName: func(bot *Bot, i *discordgo.InteractionCreate) {
			data := i.ApplicationCommandData()
			bot.handleStreamCatchCmd(i, parseOptions(data.Options))
		},
		streamcatchManageCommandName: func(bot *Bot, i *discordgo.InteractionCreate) {
			bot.handleStreamCatchManageCmd(i)
		},
	}
	componentHandlers = map[ComponentHandler]func(bot *Bot, i *discordgo.InteractionCreate, arg string){
		StopComponentHandler: func(bot *Bot, i *discordgo.InteractionCreate, streamId string) {
			bot.StopStream(i, stream.Id(streamId), func(s *stream.Stream) stream.EndedReason {
				if s.Permanent && s.Status == stream.StatusGoneLive {
					return stream.ReasonStopOneInstance
				}
				return stream.ReasonForceStopped
			})
		},
		ForceStopComponentHandler: func(bot *Bot, i *discordgo.InteractionCreate, streamId string) {
			bot.StopStream(i, stream.Id(streamId), func(s *stream.Stream) stream.EndedReason {
				return stream.ReasonForceStopped
			})
		},
		RefreshComponentHandler: func(bot *Bot, i *discordgo.InteractionCreate, arg string) {
			streamId := stream.Id(arg)
			author := interactionAuthor(i.Interaction)
			if !bot.CheckStreamAuthor(i.Interaction, streamId, author) {
				return
			}
			a, ok := bot.broadcaster.Agents()[streamId]
			if !ok {
				bot.sugar.Debugw("Agent not found", "streamId", streamId)
				content := "The stream is not available anymore."
				_, err := bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &content,
				})
				if err != nil {
					bot.sugar.Errorf("Failed to respond to interaction: %v", err)
				}
				return
			}
			newScheduledEndAt := bot.broadcaster.Config.Clock.Now().Add(ExtendDuration)
			if a.Stream.ScheduledEndAt.After(newScheduledEndAt) {
				newScheduledEndAt = a.Stream.ScheduledEndAt
			}
			err := bot.broadcaster.RefreshAgent(streamId, newScheduledEndAt)
			if err != nil {
				content := "An error occurred"
				_, err = bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &content,
				})
				if err != nil {
					bot.sugar.Errorf("Failed to respond to interaction: %v", err)
				}
				return
			}
			streamStartedMessage := bot.MakeStreamStartedMessage(a.Stream)
			content := fmt.Sprintf("<@%s>", author.ID) + streamStartedMessage.Content
			_, err = bot.session.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content:    &content,
				Components: &streamStartedMessage.Components,
				Embeds:     &streamStartedMessage.Embeds,
			})
			if err != nil {
				bot.sugar.Errorf("could not update interaction: %v", err)
			}
		},
		PermanentRecatchComponentHandler: func(bot *Bot, i *discordgo.InteractionCreate, streamUrl string) {
			bot.newStreamCatch(i.Interaction, streamUrl, true)
		},
		RecatchComponentHandler: func(bot *Bot, i *discordgo.InteractionCreate, streamUrl string) {
			bot.newStreamCatch(i.Interaction, streamUrl, false)
		},
	}
)
