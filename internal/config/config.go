package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
)

type ServerConfig struct {
	Port   string
	APIKey string
}

type GeminiConfig struct {
	Timeout           int
	ChatMode          string
	MaxChars          int
	OversizedStrategy string
	SessionTTLMinutes int
	WatchdogTimeout   int
	SnapshotStreaming bool
	Language          string
}

type StorageConfig struct {
	Path                 string
	MaxSizeMB            int
	RetentionDays        int
	CleanupIntervalHours int
}

type AccountConfig struct {
	ID     string
	PSID   string
	PSIDTS string
	Proxy  string
}

type AppConfig struct {
	Server       ServerConfig
	Gemini       GeminiConfig
	Storage      StorageConfig
	Accounts     []AccountConfig
	ModelMapping map[string]string
}

var (
	appConfig *AppConfig
	configMu  sync.RWMutex
)

func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			Port:   "8007",
			APIKey: "",
		},
		Gemini: GeminiConfig{
			Timeout:           600,
			ChatMode:          "normal",
			MaxChars:          1000000,
			OversizedStrategy: "compact",
			SessionTTLMinutes: 15,
			WatchdogTimeout:   300,
			Language:          "en",
		},
		Storage: StorageConfig{
			Path:                 "./data/sessions.db",
			MaxSizeMB:            256,
			RetentionDays:        14,
			CleanupIntervalHours: 6,
		},
	}
}

func LoadConfig() *AppConfig {
	cfg := DefaultConfig()
	log.Println("[Config] Loaded configuration from environment variables and defaults")

	applyEnvOverrides(cfg)

	configMu.Lock()
	appConfig = cfg
	configMu.Unlock()

	return cfg
}

func GetConfig() *AppConfig {
	configMu.RLock()
	defer configMu.RUnlock()
	if appConfig == nil {
		configMu.RUnlock()
		cfg := LoadConfig()
		configMu.RLock()
		return cfg
	}
	return appConfig
}

func applyEnvOverrides(cfg *AppConfig) {
	if v := os.Getenv("PORT"); v != "" {
		cfg.Server.Port = v
	}
	if v := os.Getenv("PROXY_API_KEY"); v != "" {
		cfg.Server.APIKey = v
	}

	if v := os.Getenv("GEMINI_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gemini.Timeout = n
		}
	}
	if v := os.Getenv("CHAT_MODE"); v != "" {
		cfg.Gemini.ChatMode = v
	}
	if v := os.Getenv("MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gemini.MaxChars = n
		}
	}
	if v := os.Getenv("OVERSIZED_STRATEGY"); v != "" {
		cfg.Gemini.OversizedStrategy = v
	}
	if v := os.Getenv("SESSION_TTL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gemini.SessionTTLMinutes = n
		}
	}
	if v := os.Getenv("WATCHDOG_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gemini.WatchdogTimeout = n
		}
	}
	if v := os.Getenv("LANGUAGE"); v != "" {
		cfg.Gemini.Language = v
	}
	if os.Getenv("SNAPSHOT_STREAMING") == "1" {
		cfg.Gemini.SnapshotStreaming = true
	}

	if v := os.Getenv("STORAGE_PATH"); v != "" {
		cfg.Storage.Path = v
	}
	if v := os.Getenv("STORAGE_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.MaxSizeMB = n
		}
	}
	if v := os.Getenv("RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.RetentionDays = n
		}
	}
	if v := os.Getenv("CLEANUP_INTERVAL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.CleanupIntervalHours = n
		}
	}

	if v := os.Getenv("MODEL_MAPPING"); v != "" && len(cfg.ModelMapping) == 0 {
		cfg.ModelMapping = make(map[string]string)
		pairs := strings.Split(v, ",")
		for _, pair := range pairs {
			pair = strings.TrimSpace(pair)
			parts := strings.SplitN(pair, ":", 2)
			if len(parts) == 2 {
				source := strings.TrimSpace(parts[0])
				target := strings.TrimSpace(parts[1])
				if source != "" && target != "" {
					cfg.ModelMapping[source] = target
				}
			}
		}
	}
}

func IsTemporaryChat() bool {
	cfg := GetConfig()
	return cfg.Gemini.ChatMode == "temporary"
}

func GetMaxChars() int {
	cfg := GetConfig()
	maxChars := cfg.Gemini.MaxChars
	if IsTemporaryChat() {
		maxChars = int(float64(maxChars) * 0.9)
	}
	return maxChars
}

func GetSessionTTLMinutes() int {
	return GetConfig().Gemini.SessionTTLMinutes
}
