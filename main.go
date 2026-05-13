package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gohttp "net/http"

	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/api"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/MailHog-Server/smtp"
	"github.com/mailhog/MailHog-UI/assets"
	comcfg "github.com/mailhog/MailHog/config"
	"github.com/mailhog/data"
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

	stdlog.SetOutput(&splitLogWriter{
		stdout: os.Stdout,
		file:   newMailEventLogWriter(file),
	})
	data.LogHandler = func(message string, args ...interface{}) {}
	log.Printf("Writing logs to %s", logFilePath)
}

type splitLogWriter struct {
	stdout io.Writer
	file   io.Writer
}

func (w *splitLogWriter) Write(p []byte) (int, error) {
	if w.stdout != nil {
		if _, err := w.stdout.Write(p); err != nil {
			return 0, err
		}
	}
	if w.file != nil {
		if _, err := w.file.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

type mailEventLogWriter struct {
	file io.Writer
	mu   sync.Mutex
	buf  []byte
}

func newMailEventLogWriter(file io.Writer) *mailEventLogWriter {
	return &mailEventLogWriter{file: file, buf: make([]byte, 0, 1024)}
}

func (w *mailEventLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		newline := bytes.IndexByte(w.buf, '\n')
		if newline < 0 {
			break
		}
		line := string(bytes.TrimRight(w.buf[:newline], "\r"))
		if shouldPersistMailLogLine(line) {
			if _, err := w.file.Write(append([]byte(line), '\n')); err != nil {
				return 0, err
			}
		}
		w.buf = w.buf[newline+1:]
	}
	return len(p), nil
}

func shouldPersistMailLogLine(line string) bool {
	if strings.Contains(line, "MAIL accepted:") {
		return true
	}
	if strings.Contains(line, "MAIL rejected:") {
		return true
	}
	if strings.Contains(line, "AUTH rejected:") {
		return true
	}
	if strings.Contains(line, "MAIL failed:") {
		return true
	}
	return false
}

func resolveLogFilePath() string {
	logFilePath := os.Getenv("MH_LOG_FILE")
	if logFilePath == "" {
		logFilePath = "mailhogplus.log"
	}
	if filepath.IsAbs(logFilePath) {
		return logFilePath
	}
	absPath, err := filepath.Abs(logFilePath)
	if err != nil || absPath == "" {
		return logFilePath
	}
	return absPath
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
