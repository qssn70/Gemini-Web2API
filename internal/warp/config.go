package warp

import (
	"os"
	"strings"
)

const (
	// SOCKS5Port is the default local port for the WARP SOCKS5 proxy.
	// We pick a high, unlikely-to-collide port that also hints at the
	// 1.1.1.1 service it wraps.
	SOCKS5Port = "14090"
)

type Config struct {
	// Enable controls whether the WARP tunnel should be started at all.
	// When false the module is completely inert and introduces zero
	// runtime cost.
	Enable bool

	// DeviceID holds the Cloudflare device UUID returned during
	// registration. Persisted across restarts so we don't register
	// a new device on every start.
	DeviceID string

	// PrivateKey is the base64 WireGuard private key, also persisted.
	PrivateKey string

	// Token is the bearer token returned by the registration API. Not
	// used at runtime (the WireGuard tunnel is authenticated by the
	// key itself) but kept for potential future management calls.
	Token string
}

// ReadConfig builds a WARP config from environment variables and persisted
// device state. baseDir is the storage directory (same as STORAGE_PATH's
// parent) where the device file lives.
func ReadConfig(baseDir string) Config {
	enable := strings.TrimSpace(os.Getenv("WARP_ENABLE"))
	if enable == "" || enable == "0" || strings.ToLower(enable) == "false" {
		return Config{}
	}

	cfg := Config{Enable: true}
	if d, err := loadDevice(baseDir); err == nil && d != nil {
		cfg.DeviceID = d.DeviceID
		cfg.PrivateKey = d.PrivateKey
		cfg.Token = d.Token
	}
	return cfg
}

// SaveDevice persists the registration details so the next start reuses
// the same WireGuard identity. Errors are logged but non-fatal — the
// tunnel still works, it just registers a new identity next time.
func (cfg Config) SaveDevice(baseDir string) {
	if cfg.DeviceID == "" || cfg.PrivateKey == "" {
		return
	}
	d := &deviceState{
		DeviceID:   cfg.DeviceID,
		PrivateKey: cfg.PrivateKey,
		Token:      cfg.Token,
	}
	_ = saveDevice(baseDir, d)
}
