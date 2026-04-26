package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gemini-web2api/internal/config"

	"github.com/joho/godotenv"
)

func envCandidates(cwd, execDir string) []string {
	seen := make(map[string]struct{})
	var candidates []string

	for _, dir := range []string{cwd, execDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		candidate := filepath.Join(dir, ".env")
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	return candidates
}

func loadEnvFromCandidates(candidates []string) (string, error) {
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
		if err := godotenv.Overload(candidate); err != nil {
			if fallbackErr := overloadDotEnvWithHyphenSupport(candidate, err); fallbackErr != nil {
				return "", fmt.Errorf("load %s: %w", candidate, fallbackErr)
			}
		}
		return candidate, nil
	}
	return "", nil
}

func overloadDotEnvWithHyphenSupport(path string, parseErr error) error {
	if !strings.Contains(parseErr.Error(), `unexpected character "-" in variable name`) {
		return parseErr
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("line %d: invalid env assignment %q", lineNo, scanner.Text())
		}

		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("line %d: empty env key", lineNo)
		}

		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("line %d: set %s: %w", lineNo, key, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func authStatus() (enabled bool, source string) {
	if strings.TrimSpace(os.Getenv("PROXY_API_KEY")) != "" {
		return true, "env"
	}
	cfg := config.GetConfig()
	if strings.TrimSpace(cfg.Server.APIKey) != "" {
		return true, "config"
	}
	return false, "none"
}

func loadEnvFile() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable: %w", err)
	}

	execDir := filepath.Dir(execPath)
	return loadEnvFromCandidates(envCandidates(cwd, execDir))
}
