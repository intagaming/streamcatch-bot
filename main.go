package main

import (
	"github.com/bwmarrin/discordgo"
	"log"
	"os"
	"os/signal"
	"strings"
)

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

func handleStreamCatch(s *discordgo.Session, i *discordgo.InteractionCreate, opts optionMap) {
	builder := new(strings.Builder)
	//if v, ok := opts["author"]; ok && v.BoolValue() {
	//	author := interactionAuthor(i.Interaction)
	//	builder.WriteString("**" + author.String() + "** says: ")
	//}
	builder.WriteString(opts["message"].StringValue())

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: builder.String(),
		},
	})

	if err != nil {
		log.Panicf("could not respond to interaction: %s", err)
	}
}

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "streamcatch",
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

func main() {
	botToken := os.Getenv("BOT_TOKEN")
	appId := os.Getenv("APP_ID")

	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		panic(err)
	}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}

		data := i.ApplicationCommandData()
		if data.Name == "streamcatch" {
			handleStreamCatch(s, i, parseOptions(data.Options))
		}
	})

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as %s", r.User.String())
	})

	_, err = session.ApplicationCommandBulkOverwrite(appId, "", commands)
	if err != nil {
		log.Fatalf("could not register commands: %s", err)
	}

	err = session.Open()
	if err != nil {
		log.Fatalf("could not open session: %s", err)
	}

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt)
	<-sigch

	err = session.Close()
	if err != nil {
		log.Printf("could not close session gracefully: %s", err)
	}
}
