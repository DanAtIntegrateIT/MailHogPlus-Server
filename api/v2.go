package api

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/pat"
	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/MailHog-Server/emailquality"
	"github.com/mailhog/MailHog-Server/monkey"
	"github.com/mailhog/MailHog-Server/websockets"
	"github.com/mailhog/data"
)

// APIv2 implements version 2 of the MailHog API
//
// It is currently experimental and may change in future releases.
// Use APIv1 for guaranteed compatibility.
type APIv2 struct {
	config      *config.Config
	messageChan chan *data.Message
	wsHub       *websockets.Hub
}

func createAPIv2(conf *config.Config, r *pat.Router) *APIv2 {
	log.Println("Creating API v2 with WebPath: " + conf.WebPath)
	apiv2 := &APIv2{
		config:      conf,
		messageChan: make(chan *data.Message),
		wsHub:       websockets.NewHub(),
	}

	r.Path(conf.WebPath + "/api/v2/messages").Methods("GET").HandlerFunc(apiv2.messages)
	r.Path(conf.WebPath + "/api/v2/messages").Methods("DELETE").HandlerFunc(apiv2.deleteMessages)
	r.Path(conf.WebPath + "/api/v2/messages").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/quality").Methods("GET").HandlerFunc(apiv2.messageQuality)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/quality").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/folders").Methods("GET").HandlerFunc(apiv2.folders)
	r.Path(conf.WebPath + "/api/v2/folders").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/search").Methods("GET").HandlerFunc(apiv2.search)
	r.Path(conf.WebPath + "/api/v2/search").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/jim").Methods("GET").HandlerFunc(apiv2.jim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("POST").HandlerFunc(apiv2.createJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("PUT").HandlerFunc(apiv2.updateJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("DELETE").HandlerFunc(apiv2.deleteJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("GET").HandlerFunc(apiv2.listOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/test").Methods("POST").HandlerFunc(apiv2.testOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/test").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/settings").Methods("GET").HandlerFunc(apiv2.getSettings)
	r.Path(conf.WebPath + "/api/v2/settings").Methods("PUT").HandlerFunc(apiv2.updateSettings)
	r.Path(conf.WebPath + "/api/v2/settings").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)
	r.Path(conf.WebPath + "/api/v2/logs").Methods("GET").HandlerFunc(apiv2.logs)
	r.Path(conf.WebPath + "/api/v2/logs").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/websocket").Methods("GET").HandlerFunc(apiv2.websocket)

	go func() {
		for {
			select {
			case msg := <-apiv2.messageChan:
				log.Println("Got message in APIv2 websocket channel")
				apiv2.broadcast(msg)
			}
		}
	}()

	return apiv2
}

func (apiv2 *APIv2) defaultOptions(w http.ResponseWriter, req *http.Request) {
	if len(apiv2.config.CORSOrigin) > 0 {
		w.Header().Add("Access-Control-Allow-Origin", apiv2.config.CORSOrigin)
		w.Header().Add("Access-Control-Allow-Methods", "OPTIONS,GET,PUT,POST,DELETE")
		w.Header().Add("Access-Control-Allow-Headers", "Content-Type")
	}
}

type messagesResult struct {
	Total int            `json:"total"`
	Count int            `json:"count"`
	Start int            `json:"start"`
	Items []data.Message `json:"items"`
}

type folderResult struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type foldersResponse struct {
	Count int            `json:"count"`
	Items []folderResult `json:"items"`
}

type settingsResponse struct {
	RetentionDays         int                   `json:"retentionDays"`
	StorageType           string                `json:"storageType"`
	MaildirPath           string                `json:"maildirPath"`
	SettingsFile          string                `json:"settingsFile"`
	DefaultFolders        []string              `json:"defaultFolders"`
	ForceDefaultInboxOnly bool                  `json:"forceDefaultInboxOnly"`
	OutgoingSMTP          []config.OutgoingSMTP `json:"outgoingSMTP"`
	RequiresRestart       bool                  `json:"requiresRestart"`
}

type updateSettingsRequest struct {
	RetentionDays         int                   `json:"retentionDays"`
	StorageType           string                `json:"storageType"`
	DefaultFolders        []string              `json:"defaultFolders"`
	ForceDefaultInboxOnly *bool                 `json:"forceDefaultInboxOnly"`
	OutgoingSMTP          []config.OutgoingSMTP `json:"outgoingSMTP"`
}

type outgoingSMTPTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

type logsResponse struct {
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	Count      int      `json:"count"`
	MaxLines   int      `json:"maxLines"`
	Query      string   `json:"query"`
	Configured bool     `json:"configured"`
}

const outgoingSMTPTestTimeout = 15 * time.Second

const folderHeaderName = "X-MailHogPlus-Folder"
const tagHeaderName = "X-MailHogPlus-Tags"
const legacyTagHeaderName = "X-MailHogPlus-Tag"

var smtpUsernameTagFallbackRE = regexp.MustCompile(`(?i)smtp\s+username:\s*([^\s<\r\n]+)`)
var htmlTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

func (apiv2 *APIv2) getStartLimit(w http.ResponseWriter, req *http.Request) (start, limit int) {
	start = 0
	limit = 50

	s := req.URL.Query().Get("start")
	if n, e := strconv.ParseInt(s, 10, 64); e == nil && n > 0 {
		start = int(n)
	}

	l := req.URL.Query().Get("limit")
	if n, e := strconv.ParseInt(l, 10, 64); e == nil && n > 0 {
		if n > 250 {
			n = 250
		}
		limit = int(n)
	}

	return
}

func (apiv2 *APIv2) messages(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/messages")

	apiv2.defaultOptions(w, req)

	start, limit := apiv2.getStartLimit(w, req)
	folder := strings.TrimSpace(req.URL.Query().Get("folder"))
	tag := strings.TrimSpace(req.URL.Query().Get("tag"))
	_, hasTagFilter := req.URL.Query()["tag"]
	order := normalizeMessageOrder(req.URL.Query().Get("order"))
	apiv2.applyRetention()

	var res messagesResult

	messages, err := apiv2.listAllMessages()
	if err != nil {
		panic(err)
	}

	filtered := filterMessagesByFolder(messages, folder)
	if hasTagFilter {
		filtered = filterMessagesByTag(filtered, tag)
	}
	sortMessagesByCreated(filtered, order)
	paged := pageMessages(filtered, start, limit)

	res.Count = len(paged)
	res.Start = start
	res.Items = paged
	res.Total = len(filtered)

	bytes, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "text/json")
	w.Write(bytes)
}

func (apiv2 *APIv2) messageQuality(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] GET /api/v2/messages/%s/quality\n", id)

	apiv2.defaultOptions(w, req)
	w.Header().Add("Content-Type", "application/json")

	message, err := apiv2.config.Storage.Load(id)
	if err != nil || message == nil {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"message not found"}`))
		return
	}

	res := emailquality.Score(emailQualityInputFromMessage(message))
	b, _ := json.Marshal(res)
	w.Write(b)
}

func (apiv2 *APIv2) deleteMessages(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/messages")

	apiv2.defaultOptions(w, req)
	w.Header().Add("Content-Type", "application/json")

	_, hasFolderFilter := req.URL.Query()["folder"]
	_, hasTagFilter := req.URL.Query()["tag"]
	if !hasFolderFilter && !hasTagFilter {
		if err := apiv2.config.Storage.DeleteAll(); err != nil {
			log.Printf("[APIv2] Error deleting all messages: %s", err)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"deleted":"all"}`))
		return
	}

	folder := strings.TrimSpace(req.URL.Query().Get("folder"))
	tag := strings.TrimSpace(req.URL.Query().Get("tag"))
	messages, err := apiv2.listAllMessages()
	if err != nil {
		panic(err)
	}

	targets := filterMessagesByFolder(messages, folder)
	if hasTagFilter {
		targets = filterMessagesByTag(targets, tag)
	}
	deleted := 0
	for _, m := range targets {
		if err := apiv2.config.Storage.DeleteOne(string(m.ID)); err != nil {
			log.Printf("[APIv2] Error deleting message %s: %s", m.ID, err)
			w.WriteHeader(500)
			return
		}
		deleted++
	}

	res := map[string]int{"deleted": deleted}
	b, _ := json.Marshal(res)
	w.WriteHeader(200)
	w.Write(b)
}

func (apiv2 *APIv2) search(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/search")

	apiv2.defaultOptions(w, req)

	start, limit := apiv2.getStartLimit(w, req)
	order := normalizeMessageOrder(req.URL.Query().Get("order"))

	kind := req.URL.Query().Get("kind")
	if kind != "from" && kind != "to" && kind != "containing" {
		w.WriteHeader(400)
		return
	}

	query := req.URL.Query().Get("query")
	if len(query) == 0 {
		w.WriteHeader(400)
		return
	}
	folder := strings.TrimSpace(req.URL.Query().Get("folder"))
	tag := strings.TrimSpace(req.URL.Query().Get("tag"))
	_, hasTagFilter := req.URL.Query()["tag"]
	apiv2.applyRetention()

	var res messagesResult

	max := apiv2.config.Storage.Count()
	if max == 0 {
		res.Count = 0
		res.Start = start
		res.Items = []data.Message{}
		res.Total = 0
		b, _ := json.Marshal(res)
		w.Header().Add("Content-Type", "application/json")
		w.Write(b)
		return
	}

	messages, _, err := apiv2.config.Storage.Search(kind, query, 0, max)
	if err != nil {
		panic(err)
	}

	filtered := filterMessagesByFolder([]data.Message(*messages), folder)
	if hasTagFilter {
		filtered = filterMessagesByTag(filtered, tag)
	}
	sortMessagesByCreated(filtered, order)
	paged := pageMessages(filtered, start, limit)

	res.Count = len(paged)
	res.Start = start
	res.Items = paged
	res.Total = len(filtered)

	b, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) folders(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/folders")

	apiv2.defaultOptions(w, req)
	apiv2.applyRetention()

	messages, err := apiv2.listAllMessages()
	if err != nil {
		panic(err)
	}
	items := folderResults(messages, apiv2.config.DefaultFolders)

	res := foldersResponse{
		Count: len(items),
		Items: items,
	}

	b, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) jim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	b, _ := json.Marshal(apiv2.config.Monkey)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) deleteJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	apiv2.config.Monkey = nil
}

func (apiv2 *APIv2) createJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey != nil {
		w.WriteHeader(400)
		return
	}

	apiv2.config.Monkey = config.Jim

	// Try, but ignore errors
	// Could be better (e.g., ok if no json, error if badly formed json)
	// but this works for now
	apiv2.newJimFromBody(w, req)

	w.WriteHeader(201)
}

func (apiv2 *APIv2) newJimFromBody(w http.ResponseWriter, req *http.Request) error {
	var jim monkey.Jim

	dec := json.NewDecoder(req.Body)
	err := dec.Decode(&jim)

	if err != nil {
		return err
	}

	jim.ConfigureFrom(config.Jim)

	config.Jim = &jim
	apiv2.config.Monkey = &jim

	return nil
}

func (apiv2 *APIv2) updateJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] PUT /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	err := apiv2.newJimFromBody(w, req)
	if err != nil {
		w.WriteHeader(400)
	}
}

func (apiv2 *APIv2) listOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/outgoing-smtp")

	apiv2.defaultOptions(w, req)

	b, _ := json.Marshal(config.SanitizeOutgoingSMTPMap(apiv2.config.OutgoingSMTP))
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) testOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/outgoing-smtp/test")
	apiv2.defaultOptions(w, req)
	w.Header().Add("Content-Type", "application/json")

	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		writeOutgoingSMTPTestResponse(w, 400, false, "", "unable to read request body")
		return
	}

	var in config.OutgoingSMTP
	if err := json.Unmarshal(body, &in); err != nil {
		writeOutgoingSMTPTestResponse(w, 400, false, "", "invalid request body")
		return
	}

	cfg, validationErr := normalizeOutgoingSMTPTestConfig(in)
	if validationErr != nil {
		writeOutgoingSMTPTestResponse(w, 400, false, "", validationErr.Error())
		return
	}

	if err := runOutgoingSMTPConnectivityTest(cfg); err != nil {
		writeOutgoingSMTPTestResponse(w, 400, false, "", err.Error())
		return
	}

	writeOutgoingSMTPTestResponse(w, 200, true, "SMTP server test succeeded.", "")
}

func normalizeOutgoingSMTPTestConfig(in config.OutgoingSMTP) (config.OutgoingSMTP, error) {
	out := config.OutgoingSMTP{
		Name:      strings.TrimSpace(in.Name),
		Host:      strings.TrimSpace(in.Host),
		Port:      strings.TrimSpace(in.Port),
		Username:  strings.TrimSpace(in.Username),
		Password:  in.Password,
		Mechanism: strings.ToUpper(strings.TrimSpace(in.Mechanism)),
	}

	if out.Host == "" {
		return out, fmt.Errorf("smtp host is required")
	}
	if out.Port == "" {
		return out, fmt.Errorf("smtp port is required")
	}

	switch out.Mechanism {
	case "", "NONE":
		out.Mechanism = "NONE"
		out.Username = ""
		out.Password = ""
	case "PLAIN", "CRAMMD5":
		if out.Username == "" {
			return out, fmt.Errorf("smtp username is required for %s authentication", out.Mechanism)
		}
	default:
		return out, fmt.Errorf("unsupported smtp authentication mechanism: %s", out.Mechanism)
	}

	return out, nil
}

func runOutgoingSMTPConnectivityTest(cfg config.OutgoingSMTP) error {
	client, err := newOutgoingSMTPClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := authenticateOutgoingSMTPClient(client, cfg); err != nil {
		return err
	}

	if err := client.Noop(); err != nil {
		return fmt.Errorf("smtp noop failed: %s", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit failed: %s", err)
	}

	return nil
}

func sendOutgoingSMTPMessage(cfg config.OutgoingSMTP, from string, recipients []string, message []byte) error {
	client, err := newOutgoingSMTPClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := authenticateOutgoingSMTPClient(client, cfg); err != nil {
		return err
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from failed: %s", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp recipient failed for %s: %s", recipient, err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data failed: %s", err)
	}
	if _, err := writer.Write(message); err != nil {
		writer.Close()
		return fmt.Errorf("smtp message write failed: %s", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp message close failed: %s", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit failed: %s", err)
	}
	return nil
}

func newOutgoingSMTPClient(cfg config.OutgoingSMTP) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := net.Dialer{Timeout: outgoingSMTPTestTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %s", err)
	}

	if err := conn.SetDeadline(time.Now().Add(outgoingSMTPTestTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connection deadline failed: %s", err)
	}

	if isImplicitTLSSMTPPort(cfg.Port) {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: cfg.Host})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tls handshake failed: %s", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp handshake failed: %s", err)
	}

	if err := client.Hello("mailhogplus-test"); err != nil {
		client.Close()
		return nil, fmt.Errorf("smtp hello failed: %s", err)
	}

	if !isImplicitTLSSMTPPort(cfg.Port) {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				client.Close()
				return nil, fmt.Errorf("starttls failed: %s", err)
			}
		}
	}

	return client, nil
}

func authenticateOutgoingSMTPClient(client *smtp.Client, cfg config.OutgoingSMTP) error {
	if cfg.Mechanism != "NONE" {
		hasAuth, _ := client.Extension("AUTH")
		if !hasAuth {
			return fmt.Errorf("smtp server does not support authentication")
		}

		var auth smtp.Auth
		switch cfg.Mechanism {
		case "PLAIN":
			auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		case "CRAMMD5":
			auth = smtp.CRAMMD5Auth(cfg.Username, cfg.Password)
		default:
			return fmt.Errorf("unsupported smtp authentication mechanism: %s", cfg.Mechanism)
		}

		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authentication failed: %s", err)
		}
	}
	return nil
}

func isImplicitTLSSMTPPort(port string) bool {
	return strings.TrimSpace(port) == "465"
}

func writeOutgoingSMTPTestResponse(w http.ResponseWriter, status int, success bool, message, responseError string) {
	w.WriteHeader(status)
	res := outgoingSMTPTestResponse{
		Success: success,
		Message: message,
		Error:   responseError,
	}
	b, _ := json.Marshal(res)
	w.Write(b)
}

func (apiv2 *APIv2) getSettings(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/settings")
	apiv2.defaultOptions(w, req)

	res := settingsResponse{
		RetentionDays:         apiv2.config.RetentionDays,
		StorageType:           apiv2.config.StorageType,
		MaildirPath:           apiv2.config.MaildirPath,
		SettingsFile:          apiv2.config.SettingsFile,
		DefaultFolders:        sanitizeFolderNames(apiv2.config.DefaultFolders),
		OutgoingSMTP:          config.OutgoingSMTPList(apiv2.config.OutgoingSMTP),
		ForceDefaultInboxOnly: apiv2.config.ForceDefaultInboxOnly,
		RequiresRestart:       false,
	}
	if apiv2.config.ManagedStorage != nil {
		res.RetentionDays = apiv2.config.ManagedStorage.RetentionDays()
	}

	b, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) updateSettings(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] PUT /api/v2/settings")
	apiv2.defaultOptions(w, req)

	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(400)
		return
	}

	var in updateSettingsRequest
	if err := json.Unmarshal(body, &in); err != nil {
		w.WriteHeader(400)
		return
	}
	if in.RetentionDays <= 0 {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"retentionDays must be greater than 0"}`))
		return
	}

	requiresRestart := false
	apiv2.config.RetentionDays = in.RetentionDays
	if apiv2.config.ManagedStorage != nil {
		apiv2.config.ManagedStorage.SetRetentionDays(in.RetentionDays)
		if err := apiv2.config.ManagedStorage.ApplyRetention(); err != nil {
			w.WriteHeader(500)
			return
		}
	}

	storageType := strings.TrimSpace(in.StorageType)
	if len(storageType) > 0 {
		storageType = strings.ToLower(storageType)
		if storageType != "memory" && storageType != "maildir" && storageType != "mongodb" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"storageType must be one of: memory, maildir, mongodb"}`))
			return
		}
		if apiv2.config.StorageType != storageType {
			apiv2.config.StorageType = storageType
			requiresRestart = true
		}
	}
	if in.DefaultFolders != nil {
		apiv2.config.DefaultFolders = sanitizeFolderNames(in.DefaultFolders)
	}
	if in.ForceDefaultInboxOnly != nil {
		apiv2.config.ForceDefaultInboxOnly = *in.ForceDefaultInboxOnly
	}
	if in.OutgoingSMTP != nil {
		apiv2.config.OutgoingSMTP = config.OutgoingSMTPMapFromList(in.OutgoingSMTP)
	}

	if err := apiv2.config.SaveSettings(); err != nil {
		w.WriteHeader(500)
		return
	}
	res := settingsResponse{
		RetentionDays:         apiv2.config.RetentionDays,
		StorageType:           apiv2.config.StorageType,
		MaildirPath:           apiv2.config.MaildirPath,
		SettingsFile:          apiv2.config.SettingsFile,
		DefaultFolders:        sanitizeFolderNames(apiv2.config.DefaultFolders),
		OutgoingSMTP:          config.OutgoingSMTPList(apiv2.config.OutgoingSMTP),
		ForceDefaultInboxOnly: apiv2.config.ForceDefaultInboxOnly,
		RequiresRestart:       requiresRestart,
	}
	b, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) websocket(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/websocket")

	apiv2.wsHub.Serve(w, req)
}

func (apiv2 *APIv2) broadcast(msg *data.Message) {
	log.Println("[APIv2] BROADCAST /api/v2/websocket")

	apiv2.wsHub.Broadcast(msg)
}

func (apiv2 *APIv2) logs(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/logs")
	apiv2.defaultOptions(w, req)
	w.Header().Add("Content-Type", "application/json")

	logFilePath := strings.TrimSpace(os.Getenv("MH_LOG_FILE"))
	if logFilePath == "" {
		logFilePath = "mailhogplus.log"
	}

	maxLines := 200
	if n, e := strconv.Atoi(strings.TrimSpace(req.URL.Query().Get("lines"))); e == nil && n > 0 {
		if n > 2000 {
			n = 2000
		}
		maxLines = n
	}
	query := strings.TrimSpace(req.URL.Query().Get("query"))

	matchedLines, err := tailLogLines(logFilePath, maxLines, query)
	if err != nil {
		w.WriteHeader(500)
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		w.Write(b)
		return
	}

	res := logsResponse{
		Path:       logFilePath,
		Lines:      matchedLines,
		Count:      len(matchedLines),
		MaxLines:   maxLines,
		Query:      query,
		Configured: true,
	}
	b, _ := json.Marshal(res)
	w.Write(b)
}

func tailLogLines(path string, maxLines int, query string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unable to open log file %q: %s", path, err)
	}
	defer file.Close()

	if maxLines <= 0 {
		maxLines = 200
	}
	queryLower := strings.ToLower(strings.TrimSpace(query))

	ring := make([]string, maxLines)
	total := 0
	idx := 0

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if queryLower != "" && !strings.Contains(strings.ToLower(line), queryLower) {
			continue
		}
		ring[idx] = line
		idx = (idx + 1) % maxLines
		total++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("unable to read log file %q: %s", path, err)
	}

	if total == 0 {
		return []string{}, nil
	}
	if total <= maxLines {
		return ring[:total], nil
	}

	out := make([]string, maxLines)
	for i := 0; i < maxLines; i++ {
		out[i] = ring[(idx+i)%maxLines]
	}
	return out, nil
}

func (apiv2 *APIv2) listAllMessages() ([]data.Message, error) {
	total := apiv2.config.Storage.Count()
	if total == 0 {
		return []data.Message{}, nil
	}

	messages, err := apiv2.config.Storage.List(0, total)
	if err != nil {
		return nil, err
	}
	return []data.Message(*messages), nil
}

func emailQualityInputFromMessage(message *data.Message) emailquality.EmailQualityInput {
	input := emailquality.EmailQualityInput{
		Headers: map[string][]string{},
	}
	if message == nil || message.Content == nil {
		return input
	}

	input.Headers = message.Content.Headers
	input.Subject = firstHeaderValue(message.Content.Headers, "Subject")

	if htmlPart := findMessageContentByMIME(message, "text/html"); htmlPart != nil {
		input.HTML = decodedContentBody(htmlPart)
	}
	if plainPart := findMessageContentByMIME(message, "text/plain"); plainPart != nil {
		input.PlainText = decodedContentBody(plainPart)
	}
	return input
}

func findMessageContentByMIME(message *data.Message, mimeType string) *data.Content {
	if message == nil {
		return nil
	}
	if contentMatchesMIME(message.Content, mimeType) {
		return message.Content
	}
	if message.Content != nil && message.Content.MIME != nil {
		if part := findContentInMIME(message.Content.MIME, mimeType); part != nil {
			return part
		}
	}
	if message.MIME != nil {
		return findContentInMIME(message.MIME, mimeType)
	}
	return nil
}

func findContentInMIME(mimeBody *data.MIMEBody, mimeType string) *data.Content {
	if mimeBody == nil {
		return nil
	}
	for _, part := range mimeBody.Parts {
		if contentMatchesMIME(part, mimeType) {
			return part
		}
		if part != nil && part.MIME != nil {
			if nested := findContentInMIME(part.MIME, mimeType); nested != nil {
				return nested
			}
		}
	}
	return nil
}

func contentMatchesMIME(content *data.Content, mimeType string) bool {
	if content == nil || content.Headers == nil {
		return false
	}
	contentType := firstHeaderValue(content.Headers, "Content-Type")
	return strings.Contains(strings.ToLower(contentType), strings.ToLower(mimeType))
}

func firstHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func decodedContentBody(content *data.Content) string {
	if content == nil {
		return ""
	}
	body := content.Body
	encoding := strings.ToLower(strings.TrimSpace(firstHeaderValue(content.Headers, "Content-Transfer-Encoding")))
	switch encoding {
	case "base64":
		compact := strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(body)
		decoded, err := base64.StdEncoding.DecodeString(compact)
		if err == nil {
			return string(decoded)
		}
	case "quoted-printable":
		decoded, err := ioutil.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
		if err == nil {
			return string(decoded)
		}
	}
	return body
}

func (apiv2 *APIv2) applyRetention() {
	if apiv2.config.ManagedStorage == nil {
		return
	}
	if err := apiv2.config.ManagedStorage.ApplyRetention(); err != nil {
		log.Printf("[APIv2] Error applying retention policy: %s", err)
	}
}

func filterMessagesByFolder(messages []data.Message, folder string) []data.Message {
	normalizedFolder := normalizeFolder(folder)
	filtered := make([]data.Message, 0, len(messages))
	for _, m := range messages {
		normalizedMessageFolder := normalizeFolder(folderFromMessage(m))
		if normalizedFolder == "" {
			if normalizedMessageFolder == "" {
				filtered = append(filtered, m)
			}
			continue
		}
		if normalizedMessageFolder == normalizedFolder {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func folderResults(messages []data.Message, defaultFolders []string) []folderResult {
	items := make([]folderResult, 0)
	indexByNormalizedName := map[string]int{}

	addFolder := func(folder string, count int) {
		folder = strings.TrimSpace(folder)
		normalizedFolder := normalizeFolder(folder)
		if normalizedFolder == "" {
			return
		}

		if idx, ok := indexByNormalizedName[normalizedFolder]; ok {
			items[idx].Count += count
			return
		}

		indexByNormalizedName[normalizedFolder] = len(items)
		items = append(items, folderResult{
			Name:  folder,
			Count: count,
		})
	}

	for _, folder := range sanitizeFolderNames(defaultFolders) {
		addFolder(folder, 0)
	}

	for _, m := range messages {
		addFolder(folderFromMessage(m), 1)
	}

	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	return items
}

func folderFromMessage(message data.Message) string {
	if message.Content == nil || message.Content.Headers == nil {
		return ""
	}
	for k, values := range message.Content.Headers {
		if strings.EqualFold(k, folderHeaderName) {
			if len(values) == 0 {
				return ""
			}
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func normalizeFolder(folder string) string {
	return strings.ToLower(strings.TrimSpace(folder))
}

func filterMessagesByTag(messages []data.Message, tag string) []data.Message {
	normalizedTag := normalizeTag(tag)
	filtered := make([]data.Message, 0, len(messages))
	for _, m := range messages {
		if normalizedTag == "" {
			if len(messageTagsFromMessage(m)) == 0 {
				filtered = append(filtered, m)
			}
			continue
		}
		for _, messageTag := range messageTagsFromMessage(m) {
			if normalizeTag(messageTag) == normalizedTag {
				filtered = append(filtered, m)
				break
			}
		}
	}
	return filtered
}

func tagFromMessage(message data.Message) string {
	return strings.Join(messageTagsFromMessage(message), ":")
}

func messageTagsFromMessage(message data.Message) []string {
	if message.Content == nil || message.Content.Headers == nil {
		return tagsFromMessageBodyFallback(message)
	}
	rawTags := make([]string, 0)
	rawTags = append(rawTags, tagsFromHeaderValues(message.Content.Headers, tagHeaderName)...)
	rawTags = append(rawTags, tagsFromHeaderValues(message.Content.Headers, legacyTagHeaderName)...)
	tags := sanitizeTagNames(rawTags)
	if len(tags) > 0 {
		return tags
	}
	return tagsFromMessageBodyFallback(message)
}

func tagsFromHeaderValues(headers map[string][]string, headerName string) []string {
	rawTags := make([]string, 0)
	for key, values := range headers {
		if !strings.EqualFold(key, headerName) {
			continue
		}
		for _, value := range values {
			rawTags = append(rawTags, splitTagHeaderValue(value)...)
		}
	}
	return rawTags
}

func splitTagHeaderValue(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ':' || r == ','
	})
}

func normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

func tagsFromMessageBodyFallback(message data.Message) []string {
	candidates := make([]string, 0, 3)
	if message.Content != nil && len(strings.TrimSpace(message.Content.Body)) > 0 {
		candidates = append(candidates, message.Content.Body)
	}
	if plain := findMessageContentByMIME(&message, "text/plain"); plain != nil {
		if decoded := strings.TrimSpace(decodedContentBody(plain)); len(decoded) > 0 {
			candidates = append(candidates, decoded)
		}
	}
	if html := findMessageContentByMIME(&message, "text/html"); html != nil {
		if decoded := strings.TrimSpace(decodedContentBody(html)); len(decoded) > 0 {
			candidates = append(candidates, decoded)
		}
	}

	for _, candidate := range candidates {
		cleaned := strings.TrimSpace(htmlTagStripRE.ReplaceAllString(candidate, " "))
		if len(cleaned) == 0 {
			continue
		}
		match := smtpUsernameTagFallbackRE.FindStringSubmatch(cleaned)
		if len(match) < 2 {
			continue
		}
		username := strings.TrimSpace(match[1])
		parts := strings.Split(username, ":")
		if len(parts) < 2 {
			continue
		}
		tags := sanitizeTagNames(parts[1:])
		if len(tags) > 0 {
			return tags
		}
	}

	return []string{}
}

func sanitizeTagNames(tags []string) []string {
	cleaned := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		name := strings.TrimSpace(tag)
		normalized := normalizeTag(name)
		if len(normalized) == 0 || seen[normalized] {
			continue
		}
		seen[normalized] = true
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		return []string{}
	}
	return cleaned
}

func sanitizeFolderNames(folders []string) []string {
	cleaned := make([]string, 0, len(folders))
	seen := map[string]bool{}
	for _, folder := range folders {
		name := strings.TrimSpace(folder)
		normalized := normalizeFolder(name)
		if len(normalized) == 0 || seen[normalized] {
			continue
		}
		seen[normalized] = true
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		return []string{}
	}
	return cleaned
}

func pageMessages(messages []data.Message, start, limit int) []data.Message {
	if start < 0 {
		start = 0
	}
	if limit <= 0 || start >= len(messages) {
		return []data.Message{}
	}
	end := start + limit
	if end > len(messages) {
		end = len(messages)
	}
	return messages[start:end]
}

func normalizeMessageOrder(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "asc":
		return "asc"
	default:
		return "desc"
	}
}

func sortMessagesByCreated(messages []data.Message, order string) {
	if len(messages) < 2 {
		return
	}
	order = normalizeMessageOrder(order)
	sort.SliceStable(messages, func(i, j int) bool {
		ci := messages[i].Created
		cj := messages[j].Created
		if ci.Equal(cj) {
			if order == "asc" {
				return string(messages[i].ID) < string(messages[j].ID)
			}
			return string(messages[i].ID) > string(messages[j].ID)
		}
		if order == "asc" {
			return ci.Before(cj)
		}
		return ci.After(cj)
	})
}
