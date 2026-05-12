package api

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/pat"
	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/config"
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
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/settings").Methods("GET").HandlerFunc(apiv2.getSettings)
	r.Path(conf.WebPath + "/api/v2/settings").Methods("PUT").HandlerFunc(apiv2.updateSettings)
	r.Path(conf.WebPath + "/api/v2/settings").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

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
	RetentionDays   int    `json:"retentionDays"`
	StorageType     string `json:"storageType"`
	MaildirPath     string `json:"maildirPath"`
	SettingsFile    string `json:"settingsFile"`
	RequiresRestart bool   `json:"requiresRestart"`
}

type updateSettingsRequest struct {
	RetentionDays int    `json:"retentionDays"`
	StorageType   string `json:"storageType"`
}

const folderHeaderName = "X-MailHogPlus-Folder"

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
	apiv2.applyRetention()

	var res messagesResult

	messages, err := apiv2.listAllMessages()
	if err != nil {
		panic(err)
	}

	filtered := filterMessagesByFolder(messages, folder)
	paged := pageMessages(filtered, start, limit)

	res.Count = len(paged)
	res.Start = start
	res.Items = paged
	res.Total = len(filtered)

	bytes, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "text/json")
	w.Write(bytes)
}

func (apiv2 *APIv2) deleteMessages(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/messages")

	apiv2.defaultOptions(w, req)
	w.Header().Add("Content-Type", "application/json")

	_, hasFolderFilter := req.URL.Query()["folder"]
	if !hasFolderFilter {
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
	messages, err := apiv2.listAllMessages()
	if err != nil {
		panic(err)
	}

	targets := filterMessagesByFolder(messages, folder)
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

	countByFolder := map[string]int{}
	for _, m := range messages {
		folder := folderFromMessage(m)
		if len(folder) == 0 {
			continue
		}
		countByFolder[folder]++
	}

	items := make([]folderResult, 0, len(countByFolder))
	for name, count := range countByFolder {
		items = append(items, folderResult{
			Name:  name,
			Count: count,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

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

	b, _ := json.Marshal(apiv2.config.OutgoingSMTP)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) getSettings(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/settings")
	apiv2.defaultOptions(w, req)

	res := settingsResponse{
		RetentionDays:   apiv2.config.RetentionDays,
		StorageType:     apiv2.config.StorageType,
		MaildirPath:     apiv2.config.MaildirPath,
		SettingsFile:    apiv2.config.SettingsFile,
		RequiresRestart: false,
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

	if err := apiv2.config.SaveSettings(); err != nil {
		w.WriteHeader(500)
		return
	}
	res := settingsResponse{
		RetentionDays:   apiv2.config.RetentionDays,
		StorageType:     apiv2.config.StorageType,
		MaildirPath:     apiv2.config.MaildirPath,
		SettingsFile:    apiv2.config.SettingsFile,
		RequiresRestart: requiresRestart,
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

func (apiv2 *APIv2) applyRetention() {
	if apiv2.config.ManagedStorage == nil {
		return
	}
	if err := apiv2.config.ManagedStorage.ApplyRetention(); err != nil {
		log.Printf("[APIv2] Error applying retention policy: %s", err)
	}
}

func filterMessagesByFolder(messages []data.Message, folder string) []data.Message {
	filtered := make([]data.Message, 0, len(messages))
	for _, m := range messages {
		msgFolder := folderFromMessage(m)
		if folder == "" {
			if msgFolder == "" {
				filtered = append(filtered, m)
			}
			continue
		}
		if msgFolder == folder {
			filtered = append(filtered, m)
		}
	}
	return filtered
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
