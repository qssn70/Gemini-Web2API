package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gemini-web2api/internal/config"
)

func TestEnvCandidates_PrefersWorkingDirThenExecutableDir(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "cwd")
	execDir := filepath.Join(t.TempDir(), "bin")

	got := envCandidates(cwd, execDir)

	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d (%v)", len(got), got)
	}
	if got[0] != filepath.Join(cwd, ".env") {
		t.Fatalf("expected first candidate %q, got %q", filepath.Join(cwd, ".env"), got[0])
	}
	if got[1] != filepath.Join(execDir, ".env") {
		t.Fatalf("expected second candidate %q, got %q", filepath.Join(execDir, ".env"), got[1])
	}
}

func TestLoadEnvFromCandidates_ReturnsParseErrorForInvalidDotEnv(t *testing.T) {
	invalidDir := t.TempDir()
	invalidPath := filepath.Join(invalidDir, ".env")
	if err := os.WriteFile(invalidPath, []byte("logged into Google in your browser\nPROXY_API_KEY=xxx\n"), 0o644); err != nil {
		t.Fatalf("write invalid env: %v", err)
	}

	loadedPath, err := loadEnvFromCandidates([]string{invalidPath})
	if err == nil {
		t.Fatalf("expected parse error, got nil and path %q", loadedPath)
	}
	if !strings.Contains(err.Error(), invalidPath) {
		t.Fatalf("expected error to mention %q, got %v", invalidPath, err)
	}
}

func TestLoadEnvFromCandidates_LoadsFirstExistingFile(t *testing.T) {
	const key = "ENV_LOADER_TEST_KEY"
	_ = os.Unsetenv(key)
	defer os.Unsetenv(key)

	cwdDir := t.TempDir()
	execDir := t.TempDir()
	cwdPath := filepath.Join(cwdDir, ".env")
	execPath := filepath.Join(execDir, ".env")

	if err := os.WriteFile(cwdPath, []byte(key+"=from-cwd\n"), 0o644); err != nil {
		t.Fatalf("write cwd env: %v", err)
	}
	if err := os.WriteFile(execPath, []byte(key+"=from-exec\n"), 0o644); err != nil {
		t.Fatalf("write exec env: %v", err)
	}

	loadedPath, err := loadEnvFromCandidates(envCandidates(cwdDir, execDir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loadedPath != cwdPath {
		t.Fatalf("expected loaded path %q, got %q", cwdPath, loadedPath)
	}
	if got := os.Getenv(key); got != "from-cwd" {
		t.Fatalf("expected %s=from-cwd, got %q", key, got)
	}
}

func TestLoadEnvFromCandidates_LoadsHyphenatedCookieKeysViaFallback(t *testing.T) {
	keys := []string{"__Secure-1PSID", "__Secure-1PSIDTS", "PROXY_API_KEY"}
	for _, key := range keys {
		_ = os.Unsetenv(key)
		defer os.Unsetenv(key)
	}

	envDir := t.TempDir()
	envPath := filepath.Join(envDir, ".env")
	content := strings.Join([]string{
		"__Secure-1PSID=\"psid-value\"",
		"__Secure-1PSIDTS=\"psidts-value\"",
		"PROXY_API_KEY=secret",
		"",
	}, "\n")
	if err := os.WriteFile(envPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write env: %v", err)
	}

	loadedPath, err := loadEnvFromCandidates([]string{envPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loadedPath != envPath {
		t.Fatalf("expected loaded path %q, got %q", envPath, loadedPath)
	}
	if got := os.Getenv("__Secure-1PSID"); got != "psid-value" {
		t.Fatalf("expected __Secure-1PSID to be loaded, got %q", got)
	}
	if got := os.Getenv("__Secure-1PSIDTS"); got != "psidts-value" {
		t.Fatalf("expected __Secure-1PSIDTS to be loaded, got %q", got)
	}
	if got := os.Getenv("PROXY_API_KEY"); got != "secret" {
		t.Fatalf("expected PROXY_API_KEY to be loaded, got %q", got)
	}
}

func TestAuthStatus_ReportsEnabledWhenProxyAPIKeyLoaded(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(cfgPath, []byte("PROXY_API_KEY=secret\n"), 0o644); err != nil {
		t.Fatalf("write env: %v", err)
	}
	_ = os.Unsetenv("PROXY_API_KEY")
	defer os.Unsetenv("PROXY_API_KEY")

	if _, err := loadEnvFromCandidates([]string{cfgPath}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	config.LoadConfig()

	enabled, source := authStatus()
	if !enabled {
		t.Fatalf("expected auth to be enabled")
	}
	if source == "none" {
		t.Fatalf("expected auth source not to be none")
	}
}

func TestAuthStatus_ReportsNoneWhenNoKeyLoaded(t *testing.T) {
	_ = os.Unsetenv("PROXY_API_KEY")
	config.LoadConfig()

	enabled, source := authStatus()
	if enabled {
		t.Fatalf("expected auth to be disabled")
	}
	if source != "none" {
		t.Fatalf("expected source none, got %q", source)
	}
}
