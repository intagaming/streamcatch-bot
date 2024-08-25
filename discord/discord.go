package discord

import (
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"os"
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
		panic(err)
	}

	bc := broadcaster.New(sugar)

	bot := Bot{sugar: sugar, session: session, broadcaster: bc}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}

		data := i.ApplicationCommandData()
		if data.Name == commandName {
			bot.handleStreamCatch(i, parseOptions(data.Options))
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

//func interactionAuthor(i *discordgo.Interaction) *discordgo.User {
//	if i.Member != nil {
//		return i.Member.User
//	}
//	return i.User
//}

func (bot *Bot) handleStreamCatch(i *discordgo.InteractionCreate, opts optionMap) {
	builder := new(strings.Builder)
	//if v, ok := opts["author"]; ok && v.BoolValue() {
	//	author := interactionAuthor(i.Interaction)
	//	builder.WriteString("**" + author.String() + "** says: ")
	//}
	builder.WriteString(opts["message"].StringValue())

	err := bot.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: builder.String(),
		},
	})

	if err != nil {
		bot.sugar.Panicf("could not respond to interaction: %s", err)
	}
}

const commandName = "streamcatch"

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        commandName,
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
