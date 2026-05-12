package api

import (
	gohttp "net/http"
	"time"

	"github.com/gorilla/pat"
	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/config"
)

func CreateAPI(conf *config.Config, r gohttp.Handler) {
	apiv1 := createAPIv1(conf, r.(*pat.Router))
	apiv2 := createAPIv2(conf, r.(*pat.Router))

	go func() {
		for {
			select {
			case msg := <-conf.MessageChan:
				apiv1.messageChan <- msg
				apiv2.messageChan <- msg
			}
		}
	}()

	if conf.ManagedStorage != nil {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if err := conf.ManagedStorage.ApplyRetention(); err != nil {
					log.Printf("Error applying retention policy: %s", err)
				}
			}
		}()
	}
}
