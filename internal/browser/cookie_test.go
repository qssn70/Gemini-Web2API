package browser

import (
	"os"
	"strings"
	"testing"
)

func TestCreateEnvTemplate_IncludesModernRuntimeSettings(t *testing.T) {
	tmp := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	createEnvTemplate()

	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	content := string(data)

	for _, key := range []string{
		"CHAT_MODE=normal",
		"MAX_CHARS=1000000",
		"OVERSIZED_STRATEGY=compact",
		"SESSION_TTL_MINUTES=15",
		"SNAPSHOT_STREAMING=0",
		"RETENTION_DAYS=14",
		"STORAGE_PATH=./data/sessions.db",
	} {
		if !strings.Contains(content, key) {
			t.Fatalf("expected template to contain %q", key)
		}
	}
}
