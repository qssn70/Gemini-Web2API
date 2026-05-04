package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CookieCache persists the latest known good __Secure-1PSIDTS so that, when
// the long-lived __Secure-1PSID is unchanged across restarts, we can survive
// a stale .env value (it expires every few hours) without forcing the user
// to re-extract cookies from the browser.
//
// SourcePSIDTS records what the .env supplied at the time the cache was
// created. CurrentPSIDTS is updated by the in-process rotate-cookies
// machinery. When the user manually edits .env to a NEW PSIDTS value (i.e.
// .env value differs from SourcePSIDTS), the cache is treated as stale and
// rebuilt — that way a manual override always wins over the cache.
type CookieCache struct {
	AccountID     string    `json:"account_id"`
	PSID          string    `json:"psid"`
	SourcePSIDTS  string    `json:"source_psidts"`
	CurrentPSIDTS string    `json:"current_psidts"`
	UpdatedAt     time.Time `json:"updated_at"`
}

var (
	// fileLock guards concurrent writes to the same cache file. Different
	// callsites (Init fallback, background refresher, eager cache after first
	// successful Init) can collide, and JSON files can corrupt under partial
	// writes if we don't serialize.
	fileLock sync.Mutex
)

func cookieCacheDir(baseDir string) string {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, "cookies")
}

// CookieCachePath returns the absolute path of the cache file for accountID.
// Empty accountID is normalised to "default" to match the rest of the code.
func CookieCachePath(baseDir, accountID string) string {
	id := strings.TrimSpace(accountID)
	if id == "" {
		id = "default"
	}
	// Replace separators that could escape the cookies/ subdir.
	id = strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(id)
	return filepath.Join(cookieCacheDir(baseDir), id+".json")
}

// LoadCookieCache reads the cache file for accountID. Returns (nil, nil) if
// the file does not exist (i.e. first run for this account).
func LoadCookieCache(baseDir, accountID string) (*CookieCache, error) {
	path := CookieCachePath(baseDir, accountID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cookie cache: %w", err)
	}
	var cache CookieCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parse cookie cache: %w", err)
	}
	return &cache, nil
}

// SaveCookieCache atomically writes the cache file for accountID. The file
// is created with mode 0600 because it contains session secrets.
func SaveCookieCache(baseDir string, cache *CookieCache) error {
	if cache == nil {
		return errors.New("nil cache")
	}
	if cache.UpdatedAt.IsZero() {
		cache.UpdatedAt = time.Now()
	}

	path := CookieCachePath(baseDir, cache.AccountID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("ensure cookie cache dir: %w", err)
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cookie cache: %w", err)
	}

	fileLock.Lock()
	defer fileLock.Unlock()

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write cookie cache tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Fallback for filesystems where rename onto existing file fails.
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			return fmt.Errorf("commit cookie cache: %w", err2)
		}
	}
	// Best-effort permission fix; ignore errors on platforms where it's a noop.
	_ = os.Chmod(path, 0600)
	return nil
}

// ResolvePSIDTS picks the __Secure-1PSIDTS value the runtime should use for
// an account on startup, using the rule:
//
//   - If we have a cache for the same PSID and the .env-supplied PSIDTS still
//     matches what was originally written into the cache, prefer the rotated
//     CurrentPSIDTS in the cache (it is at most a few minutes old and will
//     beat a multi-hour-old .env value).
//   - Otherwise, treat the .env value as authoritative (PSID changed, or the
//     user manually pasted a new PSIDTS).
//
// Returns the chosen value plus a short human-readable reason for logging.
func ResolvePSIDTS(envPSID, envPSIDTS string, cache *CookieCache) (string, string) {
	if cache == nil {
		return envPSIDTS, "no cache"
	}
	if cache.PSID == "" || cache.PSID != envPSID {
		return envPSIDTS, "PSID changed since cache write"
	}
	if cache.SourcePSIDTS != envPSIDTS {
		return envPSIDTS, ".env PSIDTS edited since cache write"
	}
	if cache.CurrentPSIDTS == "" {
		return envPSIDTS, "cache had no rotated value yet"
	}
	return cache.CurrentPSIDTS, fmt.Sprintf("cache (rotated %s ago)", time.Since(cache.UpdatedAt).Truncate(time.Second))
}
