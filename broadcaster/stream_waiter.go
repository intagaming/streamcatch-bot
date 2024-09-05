package broadcaster

import (
	"errors"
	"github.com/nicklaw5/helix/v2"
	"go.uber.org/zap"
)

func NewRealStreamWaiter(sugar *zap.SugaredLogger, helixClient *helix.Client, a *Agent) error {
	var err error
	switch a.Stream.Platform {
	case "twitch":
		_, err = WaitForTwitchOnline(sugar, a.ctx, helixClient, a.Stream)
	case "youtube":
		_, err = WaitForYoutubeOnline(sugar, a.ctx, a.Stream)
	case "generic":
		_, err = WaitForGenericOnline(sugar, a.ctx, a.Stream)
	case "local":
		err = WaitForLocalOnline(sugar, a.ctx, a.Stream)
	default:
		return errors.New("Unknown platform: " + a.Stream.Platform)
	}
	return err
}
