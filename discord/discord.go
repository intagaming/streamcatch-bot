package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"os"
	"streamcatch-bot/broadcaster"
	"streamcatch-bot/broadcaster/stream"
	"streamcatch-bot/broadcaster/stream/streamListener"
	"streamcatch-bot/sc_redis"
	"strings"
	"time"
)

var (
	ExtendDuration = 10 * time.Minute
	// TODO: move this to db
	//StreamAuthor   = make(map[stream.Id]*discordgo.User)
	//GuildStreams   = make(map[string]map[stream.Id]struct{})
	//StreamGuild    = make(map[stream.Id]string)
	//UserStreams    = make(map[string]map[stream.Id]struct{})
	//StreamUser     = make(map[stream.Id]string)
)

type Bot struct {
	scRedisClient                sc_redis.SCRedisClient
	sugar                        *zap.SugaredLogger
	session                      *discordgo.Session
	broadcaster                  *broadcaster.Broadcaster
	mediaServerHlsUrl            string
	mediaServerPlaybackUrl       string
	mediaServerPlaybackUrlPublic string
}

func New(sugar *zap.SugaredLogger, bc *broadcaster.Broadcaster, scRedisClient sc_redis.SCRedisClient) *Bot {
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
		scRedisClient:                scRedisClient,
	}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandsHandlers[i.ApplicationCommandData().Name]; ok {
				h(&bot, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			switch {
			case strings.HasPrefix(customID, "stop_"):
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
						},
					})
					if err != nil {
						bot.sugar.Errorf("Failed to respond to interaction: %v", err)
					}
					return
				}
				a.Close(stream.ReasonForceStopped, nil)
				err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredMessageUpdate,
				})
				if err != nil {
					bot.sugar.Errorf("Failed to respond to interaction: %v", err)
				}
			case strings.HasPrefix(customID, "refresh_"):
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
						},
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
				err = bot.broadcaster.RefreshAgent(streamId, newScheduledEndAt)
				if err != nil {
					err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "An error occurred",
						},
					})
					if err != nil {
						bot.sugar.Errorf("Failed to respond to interaction: %v", err)
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
					bot.sugar.Errorf("could not update interaction: %v", err)
				}
			case strings.HasPrefix(customID, "permanent_recatch_"):
				streamUrl := strings.TrimPrefix(customID, "permanent_recatch_")
				bot.newStreamCatch(i.Interaction, streamUrl, true)
			case strings.HasPrefix(customID, "recatch_"):
				streamUrl := strings.TrimPrefix(customID, "recatch_")
				bot.newStreamCatch(i.Interaction, streamUrl, false)
			}
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

func (bot *Bot) EditMessage(channelId string, messageId string, message string) {
	_, err := bot.session.ChannelMessageEdit(channelId, messageId, message)

	if err != nil {
		bot.sugar.Panicf("could not edit message: %s", err)
	}
}

// TODO: move this elsewhere, not in discord
func (bot *Bot) ResumeStream(redisStream *sc_redis.RedisStream) {
	ctx := context.Background()
	var message *discordgo.Message
	messageJson, err := bot.scRedisClient.GetStreamMessage(ctx, redisStream.Id)
	if err == nil {
		err = json.Unmarshal([]byte(messageJson), &message)
		if err != nil {
			bot.sugar.Errorf("could not unmarshal message: %s, deleting stream", err)
			err := bot.scRedisClient.CleanupStream(ctx, redisStream.Id)
			if err != nil {
				panic(err)
			}
			return
		}
	}
	var authorId string
	authorId, err = bot.scRedisClient.GetStreamAuthorId(ctx, redisStream.Id)
	if err != nil {
		bot.sugar.Errorf("could not get stream author: %s, deleting stream", err)
		err := bot.scRedisClient.CleanupStream(ctx, redisStream.Id)
		if err != nil {
			panic(err)
		}
		return
	}

	sl := streamListener.StreamListener{
		Sugar: bot.sugar,
		DiscordUpdater: &RealDiscordUpdater{
			bot:         bot,
			interaction: nil,
			message:     message,
			authorId:    authorId,
		},
		SCRedisClient: bot.scRedisClient,
	}
	s := stream.Stream{
		Id:                   stream.Id(redisStream.Id),
		Url:                  redisStream.Url,
		Platform:             redisStream.Platform,
		CreatedAt:            redisStream.CreatedAt,
		ScheduledEndAt:       redisStream.ScheduledEndAt,
		Status:               redisStream.Status,
		Listener:             &sl,
		ThumbnailUrl:         redisStream.ThumbnailUrl,
		Permanent:            redisStream.Permanent,
		PlatformLastStreamId: redisStream.PlatformLastStreamId,
	}
	bot.broadcaster.HandleStream(&s)
	bot.sugar.Infof("Resumed stream %s", s.Id)
}

func (bot *Bot) newStreamCatch(i *discordgo.Interaction, url string, permanent bool) {
	err := bot.session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		bot.sugar.Errorf("could not respond to interaction: %s", err)
	}

	sl := streamListener.StreamListener{
		Sugar: bot.sugar,
		DiscordUpdater: &RealDiscordUpdater{
			bot:         bot,
			interaction: i,
			message:     nil,
			authorId:    interactionAuthor(i).ID,
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
	var userId string
	if i.User != nil {
		userId = i.User.ID
	}
	err = bot.scRedisClient.SetStream(ctx, &sc_redis.SetStreamData{
		StreamId:   string(s.Id),
		StreamJson: string(sc_redis.RedisStreamFromStream(s).Marshal()),
		AuthorId:   author.ID,
		GuildId:    i.GuildID,
		UserId:     userId,
	})
	//_, err = bot.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
	//	pipe.HSet(ctx, sc_redis.StreamsKey, sc_redis.RedisStreamFromStream(s).Marshal(), 0)
	//	pipe.Set(ctx, sc_redis.StreamDiscordAuthorIdKey+string(s.Id), author.ID, 0)
	//	if i.Member != nil {
	//		pipe.SAdd(ctx, sc_redis.GuildStreamsKey+i.GuildID, s.Id)
	//		pipe.Set(ctx, sc_redis.StreamGuildKey+string(s.Id), i.GuildID, 0)
	//	} else {
	//		pipe.SAdd(ctx, sc_redis.UserStreamsKey+i.User.ID, s.Id)
	//		pipe.Set(ctx, sc_redis.StreamUserKey+string(s.Id), i.User.ID, 0)
	//	}
	//	return nil
	//})
	if err != nil {
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
	if err != nil {
		bot.sugar.Errorf("could not get stream author id: %v", err)
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
		// TODO: defer discord response
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
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "An error occurred.",
				},
			})
			if err != nil {
				bot.sugar.Errorf("Failed to respond to interaction: %v", err)
			}
			return
		}
		if len(streams) == 0 {
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "No permanent streams found.",
				},
			})
			if err != nil {
				bot.sugar.Errorf("Failed to respond to interaction: %v", err)
			}
			return
		}

		contentSb := strings.Builder{}
		contentSb.Write([]byte("Here are permanent streams that you scheduled:\n"))
		for _, streamId := range streams {
			a, ok := bot.broadcaster.Agents()[stream.Id(streamId)]
			if !ok {
				bot.sugar.Errorf("Cannot find agent from stream %s", streamId)
				continue
			}
			if !a.Stream.Permanent {
				continue
			}
			contentSb.Write([]byte(fmt.Sprintf("- Stream ID: `%s`; URL: %s\n", streamId, a.Stream.Url)))
		}

		err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: contentSb.String(),
				Flags:   discordgo.MessageFlagsSuppressEmbeds,
			},
		})
		if err != nil {
			bot.sugar.Errorf("Failed to respond to interaction: %v", err)
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
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "An error occurred.",
				},
			})
			if err != nil {
				bot.sugar.Errorf("Failed to respond to interaction: %v", err)
			}
			return
		}
		if len(streams) == 0 {
			err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "No permanent streams found.",
				},
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
		err = bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "All permanent streams have been stopped.",
			},
		})
		if err != nil {
			bot.sugar.Errorf("Failed to respond to interaction: %v", err)
		}
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
)
