package warp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const deviceFile = "warp_device.json"

type deviceState struct {
	DeviceID   string `json:"device_id"`
	PrivateKey string `json:"private_key"`
	Token      string `json:"token"`
}

var deviceMu sync.Mutex

func devicePath(baseDir string) string {
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, deviceFile)
}

func loadDevice(baseDir string) (*deviceState, error) {
	deviceMu.Lock()
	defer deviceMu.Unlock()

	data, err := os.ReadFile(devicePath(baseDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var d deviceState
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", devicePath(baseDir), err)
	}
	return &d, nil
}

func saveDevice(baseDir string, d *deviceState) error {
	deviceMu.Lock()
	defer deviceMu.Unlock()

	path := devicePath(baseDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("ensure warp device dir: %w", err)
	}

	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode warp device: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write warp device tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			return fmt.Errorf("commit warp device: %w", err2)
		}
	}
	_ = os.Chmod(path, 0600)
	return nil
}
