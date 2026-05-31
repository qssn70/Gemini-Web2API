package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gemini-web2api/internal/adapter"
	"gemini-web2api/internal/balancer"
	"gemini-web2api/internal/browser"
	"gemini-web2api/internal/config"
	"gemini-web2api/internal/gemini"
	"gemini-web2api/internal/storage"
	"gemini-web2api/internal/warp"

	"github.com/fsnotify/fsnotify"
	"github.com/gin-gonic/gin"
)

var (
	pool           *balancer.AccountPool
	store          *storage.Store
	accountConfigs map[string]string
	cookiesMu      sync.RWMutex
)

// cookieCacheBaseDir derives the directory where per-account cookie caches
// live from the storage path. We co-locate them with the sessions database
// so a single STORAGE_PATH=/data points everything at the same volume —
// this is the same pattern HanaokaYuzu/Gemini-API uses with
// GEMINI_COOKIE_PATH.
func cookieCacheBaseDir() string {
	cfg := config.GetConfig()
	if cfg == nil || cfg.Storage.Path == "" {
		return "./data"
	}
	return filepath.Dir(cfg.Storage.Path)
}

// cookieRefreshInterval is how often the background goroutine calls
// RotateCookies on each healthy account. The upstream Python client uses
// 600s (10 min) by default; we honour that and let users override via
// COOKIE_REFRESH_INTERVAL_SECONDS for tighter or looser cadences.
func cookieRefreshInterval() time.Duration {
	const defaultSeconds = 600
	v := strings.TrimSpace(os.Getenv("COOKIE_REFRESH_INTERVAL_SECONDS"))
	if v == "" {
		return time.Duration(defaultSeconds) * time.Second
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 60 {
		return time.Duration(n) * time.Second
	}
	log.Printf("[CookieRefresh] Ignoring invalid COOKIE_REFRESH_INTERVAL_SECONDS=%q, falling back to %ds", v, defaultSeconds)
	return time.Duration(defaultSeconds) * time.Second
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--fetch-cookies" {
		if err := browser.RunFetchCookies(); err != nil {
			log.Fatalf("Error: %v", err)
		}
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}
	execPath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	log.Printf("[Startup] Working directory: %s", cwd)
	log.Printf("[Startup] Executable path: %s", execPath)

	loadedEnvPath, err := loadEnvFile()
	if err != nil {
		log.Fatalf("[Startup] Failed to load .env: %v", err)
	}
	if loadedEnvPath != "" {
		log.Printf("[Startup] Loaded .env from: %s", loadedEnvPath)
	} else {
		log.Printf("[Startup] No .env found in working directory or executable directory")
	}

	cfg := config.LoadConfig()
	config.LoadModelMapping()

	authEnabled, authSource := authStatus()
	if authEnabled {
		log.Printf("[Startup] Auth enabled (source: %s)", authSource)
	} else {
		log.Printf("[Startup] Auth disabled (no PROXY_API_KEY loaded from .env or environment)")
	}

	// Start WARP tunnel if enabled. This happens before loadAccountsAsync
	// so the SOCKS5 proxy address is available when creating clients.
	warpCfg := warp.ReadConfig(cookieCacheBaseDir())
	if warpCfg.Enable {
		tunnel, warpErr := warp.Start(&warpCfg, cookieCacheBaseDir())
		if warpErr != nil {
			log.Fatalf("[Startup] WARP tunnel failed: %v", warpErr)
		}
		// Set the WARP SOCKS5 address as the default proxy so
		// loadAccountsAsync picks it up for any account that does
		// not have an explicit PROXY_* override.
		if os.Getenv("PROXY") == "" {
			os.Setenv("PROXY", tunnel.SOCKS5Addr())
			log.Printf("[Startup] WARP proxy set as default PROXY=%s", tunnel.SOCKS5Addr())
		}
	}

	pool = balancer.NewAccountPool()
	accountConfigs = make(map[string]string)

	store, err = storage.NewStore(cfg.Storage.Path)
	if err != nil {
		log.Printf("[Storage] Failed to open database: %v (session reuse disabled)", err)
	} else {
		store.StartCleanupRoutine(cfg.Storage.RetentionDays, cfg.Storage.CleanupIntervalHours)
		log.Printf("[Storage] Database opened at %s", cfg.Storage.Path)
	}

	go loadAccountsAsync()
	go watchEnvFile(filepath.Dir(execPath))
	go runCookieRefresher()

	r := gin.Default()

	r.Use(adapter.RecoveryMiddleware())
	r.Use(adapter.CORSMiddleware())
	r.Use(adapter.AuthMiddleware())
	r.Use(adapter.LoggerMiddleware())

	r.POST("/v1/chat/completions", adapter.ChatCompletionHandler(pool))
	r.POST("/v1/images/generations", adapter.ImageGenerationHandler(pool))
	r.GET("/v1/models", adapter.ListModelsHandler)
	r.POST("/v1/responses", adapter.ResponsesHandler(pool))
	r.POST("/v1/messages", adapter.ClaudeMessagesHandler(pool))
	r.POST("/v1/messages/count_tokens", adapter.ClaudeCountTokensHandler(pool))
	r.GET("/v1/models/claude", adapter.ClaudeListModelsHandler)
	r.POST("/v1beta/models/*action", adapter.GeminiRouterHandler(pool))
	r.GET("/v1beta/models", adapter.GeminiListModelsHandler)

	r.GET("/", func(c *gin.Context) {
		authEnabled, _ := authStatus()
		status := gin.H{
			"status":    "Gemini-Web2API (Go) is running",
			"docs":      "POST /v1/chat/completions (OpenAI) | POST /v1/messages (Claude) | POST /v1beta/models/{model}:generateContent (Gemini) | POST /v1/responses (Responses)",
			"protocols": []string{"openai", "claude", "gemini", "responses"},
			"accounts":  pool.Size(),
			"healthy":   pool.HealthyCount(),
			"auth":      authEnabled,
		}
		if store != nil {
			if count, err := store.Stats(); err == nil {
				status["sessions"] = count
			}
		}
		c.JSON(200, status)
	})

	port := cfg.Server.Port
	if port == "" {
		port = "8007"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	go func() {
		log.Printf("Server starting on port %s (accounts loading in background...)", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	if store != nil {
		store.Close()
		log.Println("[Storage] Database closed")
	}

	warp.Close()
	log.Println("Server exited")
}

// accountConfigHash determines whether an account needs to be re-initialised
// after a .env reload. We deliberately exclude __Secure-1PSIDTS from the
// hash because that cookie rotates server-side every few hours and is
// maintained automatically by runCookieRefresher / RotateCookies fallback in
// Init. Including it here would make every background rotation look like an
// .env edit and cause unnecessary, expensive reinitialisations (or worse, a
// reload loop if the rotation also triggered an .env write at some point).
//
// PSID and proxy URL are the values the user actually controls; those are
// the ones that legitimately should drive a reload.
func accountConfigHash(cookies map[string]string, proxyURL string) string {
	return cookies["__Secure-1PSID"] + "|" + proxyURL
}

// applyCookieCache mutates cookies in-place so the value used for NewClient
// is the freshest known PSIDTS. Returns the chosen value plus the prior
// envPSIDTS bookkeeping needed by saveCookieCacheAfterInit.
func applyCookieCache(accountID string, cookies map[string]string) (envValue, chosenValue string) {
	envValue = cookies["__Secure-1PSIDTS"]
	cache, err := storage.LoadCookieCache(cookieCacheBaseDir(), accountID)
	if err != nil {
		log.Printf("[CookieCache] Account '%s': failed to read cache (%v); falling back to .env value", displayAccountID(accountID), err)
		return envValue, envValue
	}
	chosen, reason := storage.ResolvePSIDTS(cookies["__Secure-1PSID"], envValue, cache)
	if chosen != envValue {
		log.Printf("[CookieCache] Account '%s': using __Secure-1PSIDTS from %s (suffix ...%s)", displayAccountID(accountID), reason, shortSuffix(chosen))
		cookies["__Secure-1PSIDTS"] = chosen
	} else if cache != nil {
		log.Printf("[CookieCache] Account '%s': using __Secure-1PSIDTS from .env (%s)", displayAccountID(accountID), reason)
	}
	return envValue, chosen
}

// saveCookieCacheAfterInit persists the (possibly rotated) PSIDTS held by
// client back to disk, so the next process start can skip the .env value
// when it has aged past the few-hour rotation window. We treat the .env
// value as the "source" anchor — when the user manually edits .env later,
// applyCookieCache will detect the divergence and override the cache.
func saveCookieCacheAfterInit(accountID, envPSIDTSValue string, client *gemini.Client) {
	if client == nil {
		return
	}
	current := client.Cookies["__Secure-1PSIDTS"]
	psid := client.Cookies["__Secure-1PSID"]
	if psid == "" {
		return
	}
	source := envPSIDTSValue
	if source == "" {
		source = current // best-effort anchor when .env had no value
	}
	cache := &storage.CookieCache{
		AccountID:     accountID,
		PSID:          psid,
		SourcePSIDTS:  source,
		CurrentPSIDTS: current,
	}
	if err := storage.SaveCookieCache(cookieCacheBaseDir(), cache); err != nil {
		log.Printf("[CookieCache] Account '%s': failed to save cache: %v", displayAccountID(accountID), err)
	}
}

func displayAccountID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "default"
	}
	return id
}

func shortSuffix(v string) string {
	if len(v) <= 6 {
		return v
	}
	return v[len(v)-6:]
}

func loadAccountsAsync() {
	log.Println("Loading accounts in background...")

	allCookies, accountIDs, proxyURLs, ipFamilies, err := browser.LoadMultiCookies(browser.ParseAccountIDs(os.Getenv("ACCOUNTS")))
	if err != nil {
		log.Printf("Failed to load cookies: %v", err)
		return
	}

	cfg := config.GetConfig()
	if cfg.Gemini.ChatMode == "temporary" {
		log.Println("[Config] Temporary chat mode enabled")
	}

	cookiesMu.RLock()
	oldConfigs := make(map[string]string)
	for k, v := range accountConfigs {
		oldConfigs[k] = v
	}
	cookiesMu.RUnlock()

	newConfigs := make(map[string]string)
	for i, cookies := range allCookies {
		proxyURL := ""
		if i < len(proxyURLs) {
			proxyURL = proxyURLs[i]
		}
		newConfigs[accountIDs[i]] = accountConfigHash(cookies, proxyURL)
	}

	var toInit []int
	var toKeep []string
	for i, accountID := range accountIDs {
		oldHash, existed := oldConfigs[accountID]
		newHash := newConfigs[accountID]
		if !existed || oldHash != newHash {
			toInit = append(toInit, i)
		} else {
			toKeep = append(toKeep, accountID)
		}
	}

	if len(toInit) == 0 {
		log.Println("No cookie changes detected, skipping reload")
		return
	}

	log.Printf("Detected %d account(s) with cookie changes, %d unchanged", len(toInit), len(toKeep))

	type accountResult struct {
		entry *balancer.AccountEntry
	}
	results := make(chan accountResult, len(toInit))

	var wg sync.WaitGroup

	for _, idx := range toInit {
		wg.Add(1)
		go func(i int, c map[string]string, proxyURL string, ipFamilyRaw string) {
			defer wg.Done()

			displayID := accountIDs[i]
			if displayID == "" {
				displayID = "default"
			}
			if proxyURL != "" {
				log.Printf("账号 '%s' 使用代理: %s", displayID, proxyURL)
			}

			// Resolve the IP family override. Logging uses the canonical
			// value but flags typos like "ipv44" so they don't silently
			// fall back to "auto" without the operator noticing.
			ipFamily, recognised := gemini.NormalizeIPFamily(ipFamilyRaw)
			if !recognised && strings.TrimSpace(ipFamilyRaw) != "" {
				log.Printf("账号 '%s': unrecognised IP_FAMILY=%q, falling back to auto", displayID, ipFamilyRaw)
			}
			if ipFamily != gemini.IPFamilyAuto {
				log.Printf("账号 '%s' 使用 IP family: %s", displayID, ipFamily)
			}

			// Merge any rotated __Secure-1PSIDTS we persisted in a prior run.
			// We capture the .env value first so we can record it as the
			// cache anchor after a successful Init.
			envPSIDTSValue, _ := applyCookieCache(accountIDs[i], c)

			const maxRetries = 3
			for attempt := 1; attempt <= maxRetries; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

				done := make(chan error, 1)
				var client *gemini.Client

				go func() {
					var err error
					client, err = gemini.NewClient(c, proxyURL, ipFamily)
					if err != nil {
						done <- err
						return
					}
					client.AccountID = accountIDs[i]
					client.TemporaryChat = cfg.Gemini.ChatMode == "temporary"
					done <- client.Init()
				}()

				select {
				case err := <-done:
					cancel()
					if err == nil {
						saveCookieCacheAfterInit(accountIDs[i], envPSIDTSValue, client)
						results <- accountResult{entry: &balancer.AccountEntry{Client: client, AccountID: accountIDs[i], ProxyURL: proxyURL}}
						log.Printf("Account '%s': ready", displayID)
						return
					}
					if gemini.IsSNlM0eMissingError(err) {
						// Init already tried RotateCookies internally and
						// still failed. The PSID itself is likely dead, so
						// further retries will not change the outcome.
						log.Printf("Account '%s': init failed (attempt %d/%d): %v — __Secure-1PSID may be expired, no further retries will help", displayID, attempt, maxRetries, err)
						return
					}
					if attempt < maxRetries {
						log.Printf("Account '%s': init failed (attempt %d/%d): %v, retrying in 2s...", displayID, attempt, maxRetries, err)
						time.Sleep(2 * time.Second)
					} else {
						log.Printf("Account '%s': init failed after %d attempts: %v", displayID, maxRetries, err)
					}
				case <-ctx.Done():
					cancel()
					if attempt < maxRetries {
						log.Printf("Account '%s': init timeout (attempt %d/%d), retrying in 2s...", displayID, attempt, maxRetries)
						time.Sleep(2 * time.Second)
					} else {
						log.Printf("Account '%s': init timeout after %d attempts, skipped", displayID, maxRetries)
					}
				}
			}
		}(idx, allCookies[idx], proxyURLs[idx], ipFamilies[idx])
	}

	wg.Wait()
	close(results)

	changedAccounts := make(map[string]*balancer.AccountEntry)
	for result := range results {
		changedAccounts[result.entry.AccountID] = result.entry
	}

	pool.ReplaceAccounts(accountIDs, changedAccounts)

	cookiesMu.Lock()
	accountConfigs = newConfigs
	cookiesMu.Unlock()

	log.Printf("Total %d account(s) available for load balancing (%d healthy)", pool.Size(), pool.HealthyCount())
}

// runCookieRefresher periodically calls RotateCookies on every account in
// the pool so that long-running deployments never let __Secure-1PSIDTS
// drift past Google's rotation window. Each successful rotation is also
// persisted to the on-disk cookie cache, which is what lets the next
// process start tolerate an .env file whose PSIDTS aged out hours ago.
//
// Rate-limit notes:
//   - The cookie cache file is written with mtime = now; we re-check it
//     against a 60s floor to avoid 429s if the interval is misconfigured.
//   - Errors are logged but never fatal — the server keeps serving
//     existing requests; the next tick will retry.
func runCookieRefresher() {
	interval := cookieRefreshInterval()
	log.Printf("[CookieRefresh] Background __Secure-1PSIDTS rotator scheduled every %s", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		entries := pool.Snapshot()
		if len(entries) == 0 {
			continue
		}
		for _, entry := range entries {
			if entry == nil || entry.Client == nil {
				continue
			}
			displayID := displayAccountID(entry.AccountID)

			// Look up the cache anchor (SourcePSIDTS) so we can preserve
			// the user's original .env value even after many rotations.
			cache, _ := storage.LoadCookieCache(cookieCacheBaseDir(), entry.AccountID)
			source := ""
			if cache != nil {
				source = cache.SourcePSIDTS
			}
			if source == "" {
				// Fall back to the current cookie if we have nothing else.
				source = entry.Client.Cookies["__Secure-1PSIDTS"]
			}

			newTS, err := entry.Client.RotateCookies()
			if err != nil {
				if _, isAuth := err.(*gemini.ErrRotateAuth); isAuth {
					log.Printf("[CookieRefresh] Account '%s': rotate returned 401, __Secure-1PSID likely expired — manual cookie update required", displayID)
					continue
				}
				log.Printf("[CookieRefresh] Account '%s': rotate failed: %v (will retry next tick)", displayID, err)
				continue
			}

			updated := &storage.CookieCache{
				AccountID:     entry.AccountID,
				PSID:          entry.Client.Cookies["__Secure-1PSID"],
				SourcePSIDTS:  source,
				CurrentPSIDTS: newTS,
			}
			if saveErr := storage.SaveCookieCache(cookieCacheBaseDir(), updated); saveErr != nil {
				log.Printf("[CookieRefresh] Account '%s': rotated but failed to persist cache: %v", displayID, saveErr)
				continue
			}
			log.Printf("[CookieRefresh] Account '%s': __Secure-1PSIDTS refreshed (suffix ...%s)", displayID, shortSuffix(newTS))
		}
	}
}

func watchEnvFile(execDir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed to create file watcher: %v", err)
		return
	}
	defer watcher.Close()

	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Failed to get working directory for watcher: %v", err)
		return
	}

	candidates := envCandidates(cwd, execDir)
	watched := false
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			if err := watcher.Add(candidate); err == nil {
				log.Printf("Watching %s for changes...", candidate)
				watched = true
			}
		}
	}
	if !watched {
		log.Printf("No .env file available to watch in working directory or executable directory")
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("%s changed, reloading environment...", event.Name)
				time.Sleep(200 * time.Millisecond)
				loadedPath, err := loadEnvFile()
				if err != nil {
					log.Printf("[Startup] Failed to reload .env: %v", err)
					continue
				}
				if loadedPath != "" {
					log.Printf("[Startup] Reloaded .env from: %s", loadedPath)
				}
				config.LoadConfig()
				config.LoadModelMapping()
				loadAccountsAsync()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}
