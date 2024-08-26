package main

import (
	"flag"
	"go.uber.org/zap"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"streamcatch-bot/discord"
)

var isDev bool

const (
	devUsage = "whether to run in dev mode, which prints debug logs"
)

func init() {
	flag.BoolVar(&isDev, "dev", false, devUsage)
	flag.BoolVar(&isDev, "d", false, devUsage+" (shorthand)")
}

func main() {
	flag.Parse()

	var logger *zap.Logger
	var err error
	if isDev {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Sync()
	sugar := logger.Sugar()

	if _, err := exec.LookPath("streamlink"); err != nil {
		sugar.Panic("streamlink not found")
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		sugar.Panic("ffmpeg not found")
	}

	bot := discord.New(sugar)

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt)
	<-sigch

	err = bot.Close()
	if err != nil {
		sugar.Infof("could not close session gracefully: %s", err)
	}
}
