package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gemini-web2api/internal/adapter"
	"gemini-web2api/internal/balancer"
	"gemini-web2api/internal/browser"
	"gemini-web2api/internal/config"
	"gemini-web2api/internal/gemini"
	"gemini-web2api/internal/storage"

	"github.com/fsnotify/fsnotify"
	"github.com/gin-gonic/gin"
)

var (
	pool           *balancer.AccountPool
	store          *storage.Store
	accountConfigs map[string]string
	cookiesMu      sync.RWMutex
)

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

	log.Println("Server exited")
}

func accountConfigHash(cookies map[string]string, proxyURL string) string {
	return cookies["__Secure-1PSID"] + "|" + cookies["__Secure-1PSIDTS"] + "|" + proxyURL
}

func loadAccountsAsync() {
	log.Println("Loading accounts in background...")

	allCookies, accountIDs, proxyURLs, err := browser.LoadMultiCookies(browser.ParseAccountIDs(os.Getenv("ACCOUNTS")))
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
		go func(i int, c map[string]string, proxyURL string) {
			defer wg.Done()

			displayID := accountIDs[i]
			if displayID == "" {
				displayID = "default"
			}
			if proxyURL != "" {
				log.Printf("账号 '%s' 使用代理: %s", displayID, proxyURL)
			}

			const maxRetries = 3
			for attempt := 1; attempt <= maxRetries; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

				done := make(chan error, 1)
				var client *gemini.Client

				go func() {
					var err error
					client, err = gemini.NewClient(c, proxyURL)
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
						results <- accountResult{entry: &balancer.AccountEntry{Client: client, AccountID: accountIDs[i], ProxyURL: proxyURL}}
						log.Printf("Account '%s': ready", displayID)
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
		}(idx, allCookies[idx], proxyURLs[idx])
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
