package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_UsesEnvironmentVariablesOnly(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("PROXY_API_KEY", "env-key")
	t.Setenv("GEMINI_TIMEOUT", "123")
	t.Setenv("CHAT_MODE", "temporary")
	t.Setenv("MAX_CHARS", "456789")
	t.Setenv("OVERSIZED_STRATEGY", "truncate")
	t.Setenv("SESSION_TTL_MINUTES", "42")
	t.Setenv("LANGUAGE", "zh")
	t.Setenv("SNAPSHOT_STREAMING", "1")
	t.Setenv("STORAGE_PATH", "/tmp/sessions.db")
	t.Setenv("STORAGE_MAX_SIZE_MB", "512")
	t.Setenv("RETENTION_DAYS", "30")
	t.Setenv("MODEL_MAPPING", "gemini-2.5-pro:claude-4-sonnet, gemini-2.5-flash:claude-4-haiku")

	cfg := LoadConfig()

	if cfg.Server.Port != "9090" {
		t.Fatalf("expected PORT override, got %q", cfg.Server.Port)
	}
	if cfg.Server.APIKey != "env-key" {
		t.Fatalf("expected PROXY_API_KEY override, got %q", cfg.Server.APIKey)
	}
	if cfg.Gemini.Timeout != 123 {
		t.Fatalf("expected GEMINI_TIMEOUT override, got %d", cfg.Gemini.Timeout)
	}
	if cfg.Gemini.ChatMode != "temporary" {
		t.Fatalf("expected CHAT_MODE override, got %q", cfg.Gemini.ChatMode)
	}
	if cfg.Gemini.MaxChars != 456789 {
		t.Fatalf("expected MAX_CHARS override, got %d", cfg.Gemini.MaxChars)
	}
	if cfg.Gemini.OversizedStrategy != "truncate" {
		t.Fatalf("expected OVERSIZED_STRATEGY override, got %q", cfg.Gemini.OversizedStrategy)
	}
	if cfg.Gemini.SessionTTLMinutes != 42 {
		t.Fatalf("expected SESSION_TTL_MINUTES override, got %d", cfg.Gemini.SessionTTLMinutes)
	}
	if cfg.Gemini.Language != "zh" {
		t.Fatalf("expected LANGUAGE override, got %q", cfg.Gemini.Language)
	}
	if !cfg.Gemini.SnapshotStreaming {
		t.Fatalf("expected SNAPSHOT_STREAMING=1 to set SnapshotStreaming true")
	}
	if cfg.Storage.Path != "/tmp/sessions.db" {
		t.Fatalf("expected STORAGE_PATH override, got %q", cfg.Storage.Path)
	}
	if cfg.Storage.MaxSizeMB != 512 {
		t.Fatalf("expected STORAGE_MAX_SIZE_MB override, got %d", cfg.Storage.MaxSizeMB)
	}
	if cfg.Storage.RetentionDays != 30 {
		t.Fatalf("expected RETENTION_DAYS override, got %d", cfg.Storage.RetentionDays)
	}
	if cfg.ModelMapping["gemini-2.5-pro"] != "claude-4-sonnet" {
		t.Fatalf("expected MODEL_MAPPING for gemini-2.5-pro, got %q", cfg.ModelMapping["gemini-2.5-pro"])
	}
	if cfg.ModelMapping["gemini-2.5-flash"] != "claude-4-haiku" {
		t.Fatalf("expected MODEL_MAPPING for gemini-2.5-flash, got %q", cfg.ModelMapping["gemini-2.5-flash"])
	}
}

func TestLoadConfig_IgnoresConfigYAML(t *testing.T) {
	t.Setenv("PORT", "")

	tmpDir := t.TempDir()
	content := []byte("server:\n  port: \"9999\"\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("failed to write config.yaml fixture: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to temp dir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	cfg := LoadConfig()

	if cfg.Server.Port != "8007" {
		t.Fatalf("expected default port 8007 when config.yaml is ignored, got %q", cfg.Server.Port)
	}
}
