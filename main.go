package main

import (
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"time"

	gohttp "net/http"

	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/api"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/MailHog-Server/smtp"
	"github.com/mailhog/MailHog-UI/assets"
	comcfg "github.com/mailhog/MailHog/config"
	"github.com/mailhog/http"
)

var conf *config.Config
var comconf *comcfg.Config
var exitCh chan int

func configureLogging() {
	logFilePath := resolveLogFilePath()

	if err := rotateLogFileIfOlderThan(logFilePath, 24*time.Hour); err != nil {
		log.Printf("Unable to rotate log file %q: %s", logFilePath, err)
	}

	file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Unable to open log file %q: %s", logFilePath, err)
		return
	}

	stdlog.SetOutput(io.MultiWriter(os.Stdout, file))
	log.Printf("Writing logs to %s", logFilePath)
}

func resolveLogFilePath() string {
	logFilePath := os.Getenv("MH_LOG_FILE")
	if logFilePath == "" {
		logFilePath = "mailhogplus.log"
	}
	if filepath.IsAbs(logFilePath) {
		return logFilePath
	}
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return logFilePath
	}
	return filepath.Join(filepath.Dir(exePath), logFilePath)
}

func rotateLogFileIfOlderThan(logFilePath string, maxAge time.Duration) error {
	info, err := os.Stat(logFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if time.Since(info.ModTime()) < maxAge {
		return nil
	}

	rotatedPath := fmt.Sprintf("%s.%s", logFilePath, info.ModTime().Format("20060102"))
	if err := os.Remove(rotatedPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(logFilePath, rotatedPath); err != nil {
		return err
	}

	entries, err := findOlderRotatedLogFiles(logFilePath, time.Now().Add(-maxAge))
	if err != nil {
		return err
	}
	for _, path := range entries {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func findOlderRotatedLogFiles(basePath string, cutoff time.Time) ([]string, error) {
	matches, err := filepath.Glob(basePath + ".*")
	if err != nil {
		return nil, err
	}
	old := make([]string, 0, len(matches))
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, statErr
		}
		if info.ModTime().Before(cutoff) {
			old = append(old, path)
		}
	}
	return old, nil
}

func configure() {
	comcfg.RegisterFlags()
	config.RegisterFlags()
	flag.Parse()
	conf = config.Configure()
	comconf = comcfg.Configure()
}

func main() {
	configure()
	configureLogging()

	if comconf.AuthFile != "" {
		http.AuthFile(comconf.AuthFile)
	}

	exitCh = make(chan int)
	cb := func(r gohttp.Handler) {
		api.CreateAPI(conf, r)
	}
	go http.Listen(conf.APIBindAddr, assets.Asset, exitCh, cb)
	go smtp.Listen(conf, exitCh)

	for {
		select {
		case <-exitCh:
			log.Printf("Received exit signal")
			os.Exit(0)
		}
	}
}
