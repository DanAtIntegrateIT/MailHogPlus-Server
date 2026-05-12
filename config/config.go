package config

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/ian-kent/envconf"
	"github.com/mailhog/MailHog-Server/monkey"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// DefaultConfig is the default config
func DefaultConfig() *Config {
	return &Config{
		SMTPBindAddr:          "0.0.0.0:1025",
		APIBindAddr:           "0.0.0.0:8025",
		Hostname:              "mailhog.example",
		MongoURI:              "127.0.0.1:27017",
		MongoDb:               "mailhog",
		MongoColl:             "messages",
		MaildirPath:           "mailhogplus-data",
		StorageType:           "maildir",
		CORSOrigin:            "",
		WebPath:               "",
		MessageChan:           make(chan *data.Message),
		OutgoingSMTP:          make(map[string]*OutgoingSMTP),
		SettingsFile:          "mailhogplus-settings.json",
		RetentionDays:         10,
		DefaultFolders:        []string{},
		ForceDefaultInboxOnly: false,
	}
}

// Config is the config, kind of
type Config struct {
	SMTPBindAddr          string
	APIBindAddr           string
	Hostname              string
	MongoURI              string
	MongoDb               string
	MongoColl             string
	StorageType           string
	CORSOrigin            string
	MaildirPath           string
	InviteJim             bool
	Storage               storage.Storage
	MessageChan           chan *data.Message
	Assets                func(asset string) ([]byte, error)
	Monkey                monkey.ChaosMonkey
	OutgoingSMTPFile      string
	OutgoingSMTP          map[string]*OutgoingSMTP
	WebPath               string
	SettingsFile          string
	RetentionDays         int
	DefaultFolders        []string
	ForceDefaultInboxOnly bool
	ManagedStorage        *ManagedStorage
	settingsMu            sync.RWMutex
}

// OutgoingSMTP is an outgoing SMTP server config
type OutgoingSMTP struct {
	Name      string
	Save      bool
	Email     string
	Host      string
	Port      string
	Username  string
	Password  string
	Mechanism string
}

var cfg = DefaultConfig()

type persistedSettings struct {
	RetentionDays         int                      `json:"retentionDays"`
	StorageType           string                   `json:"storageType"`
	DefaultFolders        []string                 `json:"defaultFolders"`
	ForceDefaultInboxOnly bool                     `json:"forceDefaultInboxOnly"`
	OutgoingSMTP          map[string]*OutgoingSMTP `json:"outgoingSMTP"`
}

// Jim is a monkey
var Jim = &monkey.Jim{}

// Configure configures stuff
func Configure() *Config {
	cfg.loadSettings()

	switch cfg.StorageType {
	case "memory":
		log.Println("Using in-memory storage")
		cfg.Storage = storage.CreateInMemory()
	case "mongodb":
		log.Println("Using MongoDB message storage")
		s := storage.CreateMongoDB(cfg.MongoURI, cfg.MongoDb, cfg.MongoColl)
		if s == nil {
			log.Println("MongoDB storage unavailable, reverting to in-memory storage")
			cfg.Storage = storage.CreateInMemory()
		} else {
			log.Println("Connected to MongoDB")
			cfg.Storage = s
		}
	case "maildir":
		log.Println("Using maildir message storage")
		s := storage.CreateMaildir(cfg.MaildirPath)
		cfg.Storage = s
	default:
		log.Fatalf("Invalid storage type %s", cfg.StorageType)
	}

	cfg.ManagedStorage = NewManagedStorage(cfg.Storage, cfg.RetentionDays)
	cfg.Storage = cfg.ManagedStorage
	if err := cfg.ManagedStorage.ApplyRetention(); err != nil {
		log.Printf("Error applying startup retention policy: %s", err)
	}

	Jim.Configure(func(message string, args ...interface{}) {
		log.Printf(message, args...)
	})
	if cfg.InviteJim {
		cfg.Monkey = Jim
	}

	if len(cfg.OutgoingSMTPFile) > 0 {
		b, err := ioutil.ReadFile(cfg.OutgoingSMTPFile)
		if err != nil {
			log.Fatal(err)
		}
		var o map[string]*OutgoingSMTP
		err = json.Unmarshal(b, &o)
		if err != nil {
			log.Fatal(err)
		}
		cfg.OutgoingSMTP = SanitizeOutgoingSMTPMap(o)
	}
	cfg.OutgoingSMTP = SanitizeOutgoingSMTPMap(cfg.OutgoingSMTP)

	return cfg
}

// RegisterFlags registers flags
func RegisterFlags() {
	flag.StringVar(&cfg.SMTPBindAddr, "smtp-bind-addr", envconf.FromEnvP("MH_SMTP_BIND_ADDR", "0.0.0.0:1025").(string), "SMTP bind interface and port, e.g. 0.0.0.0:1025 or just :1025")
	flag.StringVar(&cfg.APIBindAddr, "api-bind-addr", envconf.FromEnvP("MH_API_BIND_ADDR", "0.0.0.0:8025").(string), "HTTP bind interface and port for API, e.g. 0.0.0.0:8025 or just :8025")
	flag.StringVar(&cfg.Hostname, "hostname", envconf.FromEnvP("MH_HOSTNAME", "mailhog.example").(string), "Hostname for EHLO/HELO response, e.g. mailhog.example")
	flag.StringVar(&cfg.StorageType, "storage", envconf.FromEnvP("MH_STORAGE", "maildir").(string), "Message storage: 'memory', 'mongodb' or 'maildir' (default)")
	flag.StringVar(&cfg.MongoURI, "mongo-uri", envconf.FromEnvP("MH_MONGO_URI", "127.0.0.1:27017").(string), "MongoDB URI, e.g. 127.0.0.1:27017")
	flag.StringVar(&cfg.MongoDb, "mongo-db", envconf.FromEnvP("MH_MONGO_DB", "mailhog").(string), "MongoDB database, e.g. mailhog")
	flag.StringVar(&cfg.MongoColl, "mongo-coll", envconf.FromEnvP("MH_MONGO_COLLECTION", "messages").(string), "MongoDB collection, e.g. messages")
	flag.StringVar(&cfg.CORSOrigin, "cors-origin", envconf.FromEnvP("MH_CORS_ORIGIN", "").(string), "CORS Access-Control-Allow-Origin header for API endpoints")
	flag.StringVar(&cfg.MaildirPath, "maildir-path", envconf.FromEnvP("MH_MAILDIR_PATH", "mailhogplus-data").(string), "Maildir path (if storage type is 'maildir')")
	flag.BoolVar(&cfg.InviteJim, "invite-jim", envconf.FromEnvP("MH_INVITE_JIM", false).(bool), "Decide whether to invite Jim (beware, he causes trouble)")
	flag.StringVar(&cfg.OutgoingSMTPFile, "outgoing-smtp", envconf.FromEnvP("MH_OUTGOING_SMTP", "").(string), "JSON file containing outgoing SMTP servers")
	flag.StringVar(&cfg.SettingsFile, "settings-file", envconf.FromEnvP("MH_SETTINGS_FILE", "mailhogplus-settings.json").(string), "Settings JSON path (retention and server behavior)")
	flag.IntVar(&cfg.RetentionDays, "retention-days", envconf.FromEnvP("MH_RETENTION_DAYS", 10).(int), "Message retention period in days (default 10)")
	flag.BoolVar(&cfg.ForceDefaultInboxOnly, "force-default-inbox-only", envconf.FromEnvP("MH_FORCE_DEFAULT_INBOX_ONLY", false).(bool), "Reject SMTP auth usernames that are not in default inbox folders")
	Jim.RegisterFlags()
}

func (cfg *Config) loadSettings() {
	if len(cfg.SettingsFile) == 0 {
		return
	}
	if _, err := os.Stat(cfg.SettingsFile); err != nil {
		return
	}

	b, err := ioutil.ReadFile(cfg.SettingsFile)
	if err != nil {
		log.Printf("Unable to read settings file %s: %s", cfg.SettingsFile, err)
		return
	}

	var s persistedSettings
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("Unable to parse settings file %s: %s", cfg.SettingsFile, err)
		return
	}

	if s.RetentionDays > 0 {
		cfg.RetentionDays = s.RetentionDays
	}
	if isValidStorageType(s.StorageType) {
		cfg.StorageType = s.StorageType
	}
	cfg.DefaultFolders = sanitizeFolderNames(s.DefaultFolders)
	cfg.ForceDefaultInboxOnly = s.ForceDefaultInboxOnly
	cfg.OutgoingSMTP = SanitizeOutgoingSMTPMap(s.OutgoingSMTP)
}

func (cfg *Config) SaveSettings() error {
	cfg.settingsMu.Lock()
	defer cfg.settingsMu.Unlock()

	s := persistedSettings{
		RetentionDays:         cfg.RetentionDays,
		StorageType:           cfg.StorageType,
		DefaultFolders:        sanitizeFolderNames(cfg.DefaultFolders),
		ForceDefaultInboxOnly: cfg.ForceDefaultInboxOnly,
		OutgoingSMTP:          SanitizeOutgoingSMTPMap(cfg.OutgoingSMTP),
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(cfg.SettingsFile, b, 0644)
}

func isValidStorageType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "memory", "maildir", "mongodb":
		return true
	default:
		return false
	}
}

func sanitizeFolderNames(folders []string) []string {
	cleaned := make([]string, 0, len(folders))
	seen := map[string]bool{}
	for _, folder := range folders {
		name := strings.TrimSpace(folder)
		normalized := strings.ToLower(name)
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

// SanitizeOutgoingSMTPMap removes invalid outgoing SMTP entries and normalizes values.
func SanitizeOutgoingSMTPMap(servers map[string]*OutgoingSMTP) map[string]*OutgoingSMTP {
	cleaned := make(map[string]*OutgoingSMTP)
	for name, server := range servers {
		normalized := normalizeOutgoingSMTPServer(name, server)
		if normalized == nil {
			continue
		}
		cleaned[normalized.Name] = normalized
	}
	return cleaned
}

// OutgoingSMTPMapFromList converts a list of outgoing SMTP servers into a name-keyed map.
func OutgoingSMTPMapFromList(servers []OutgoingSMTP) map[string]*OutgoingSMTP {
	cleaned := make(map[string]*OutgoingSMTP)
	for i := range servers {
		normalized := normalizeOutgoingSMTPServer("", &servers[i])
		if normalized == nil {
			continue
		}
		cleaned[normalized.Name] = normalized
	}
	return cleaned
}

// OutgoingSMTPList converts a name-keyed outgoing SMTP map into a sorted list.
func OutgoingSMTPList(servers map[string]*OutgoingSMTP) []OutgoingSMTP {
	cleanedMap := SanitizeOutgoingSMTPMap(servers)
	cleanedList := make([]OutgoingSMTP, 0, len(cleanedMap))
	for _, server := range cleanedMap {
		if server == nil {
			continue
		}
		cleanedList = append(cleanedList, *server)
	}
	sort.Slice(cleanedList, func(i, j int) bool {
		return strings.ToLower(cleanedList[i].Name) < strings.ToLower(cleanedList[j].Name)
	})
	return cleanedList
}

func normalizeOutgoingSMTPServer(fallbackName string, server *OutgoingSMTP) *OutgoingSMTP {
	if server == nil {
		return nil
	}

	name := strings.TrimSpace(server.Name)
	if len(name) == 0 {
		name = strings.TrimSpace(fallbackName)
	}
	host := strings.TrimSpace(server.Host)
	port := strings.TrimSpace(server.Port)
	if len(name) == 0 || len(host) == 0 || len(port) == 0 {
		return nil
	}

	mechanism := strings.ToUpper(strings.TrimSpace(server.Mechanism))
	switch mechanism {
	case "", "NONE":
		mechanism = "NONE"
	case "PLAIN", "CRAMMD5":
	default:
		mechanism = "NONE"
	}

	username := strings.TrimSpace(server.Username)
	password := server.Password
	if mechanism == "NONE" {
		username = ""
		password = ""
	}

	return &OutgoingSMTP{
		Name:      name,
		Save:      false,
		Email:     strings.TrimSpace(server.Email),
		Host:      host,
		Port:      port,
		Username:  username,
		Password:  password,
		Mechanism: mechanism,
	}
}

func (cfg *Config) IsFolderAllowed(folder string) bool {
	if !cfg.ForceDefaultInboxOnly {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(folder))
	if normalized == "" {
		return false
	}
	for _, allowed := range cfg.DefaultFolders {
		if strings.ToLower(strings.TrimSpace(allowed)) == normalized {
			return true
		}
	}
	return false
}
