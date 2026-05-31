package warp

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/curve25519"
)

// warpAPIBase is the Cloudflare WARP client API base URL. The version
// suffix is the build number of the 1.1.1.1 app we pretend to be.
// wgcf (the reference implementation) currently uses v0a1922.
const (
	warpAPIBase = "https://api.cloudflareclient.com"
	warpAPIVer  = "v0a1922"
)

// warpRegisterResp models the JSON returned by POST /{ver}/reg.
// The wireguard peer and interface config live inside "config".
type warpRegisterResp struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Account struct {
		ID      string `json:"id"`
		License string `json:"license"`
	} `json:"account"`
	Config warpConfig `json:"config"`
}

// warpGetDeviceResp models the JSON returned by GET /{ver}/reg/{deviceId}.
// Structurally identical to RegisterResp for our purposes — both contain
// the Config block with peers and interface addresses.
type warpGetDeviceResp = warpRegisterResp

type warpConfig struct {
	ClientID  string           `json:"client_id"`
	Interface warpInterface    `json:"interface"`
	Peers     []warpPeer       `json:"peers"`
}

type warpInterface struct {
	Addresses warpAddresses `json:"addresses"`
}

type warpAddresses struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

type warpPeer struct {
	PublicKey string          `json:"public_key"`
	Endpoint  warpEndpointInfo `json:"endpoint"`
}

type warpEndpointInfo struct {
	Host string `json:"host"` // e.g. "engage.cloudflareclient.com:2408"
	V4   string `json:"v4"`   // e.g. "162.159.192.1"
	V6   string `json:"v6"`   // e.g. "[2606:4700:d0::1]"
}

// warpHTTPClient is a dedicated HTTP client with TLS1.2 settings that
// match the real 1.1.1.1 app. The WARP API rejects connections that
// don't match the expected TLS fingerprint.
var warpHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		ForceAttemptHTTP2: false,
		// TLS 1.2 only — the API returns 403 otherwise.
		TLSClientConfig: tls12Config(),
	},
}

// Register creates a new device with the Cloudflare WARP API and
// populates cfg.DeviceID, cfg.PrivateKey and cfg.Token.
// After registration, a follow-up GET fetches the WireGuard peer
// config which is returned for the caller to set up the tunnel.
func (cfg *Config) Register() (*warpConfig, error) {
	var priv [32]byte
	if _, err := randomBytes(priv[:]); err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"key":        base64.StdEncoding.EncodeToString(pub),
		"install_id": randomSerial(),
		"fcm_token":  "",
		"tos":        time.Now().UTC().Format(time.RFC3339),
		"model":      "Linux",
		"serial":     randomSerial(),
		"locale":     "en_US",
		"type":       "Android",
	})

	regURL := fmt.Sprintf("%s/%s/reg", warpAPIBase, warpAPIVer)
	req, _ := http.NewRequest(http.MethodPost, regURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.3-1922")

	resp, err := warpHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("register request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("register API returned %d: %s", resp.StatusCode, truncate(string(respBody), 400))
	}

	// Debug: log raw response so we can diagnose format changes.
	log.Printf("[WARP] Register response (first 800 chars): %s", truncate(string(respBody), 800))

	var reg warpRegisterResp
	if err := json.Unmarshal(respBody, &reg); err != nil {
		return nil, fmt.Errorf("parse register response: %w", err)
	}

	cfg.DeviceID = reg.ID
	if cfg.DeviceID == "" {
		cfg.DeviceID = reg.Account.ID
	}
	cfg.PrivateKey = base64.StdEncoding.EncodeToString(priv[:])
	cfg.Token = reg.Token

	log.Printf("[WARP] Registered: device=%s config.peers=%d config.interface.v4=%s",
		cfg.DeviceID[:minLen(8, len(cfg.DeviceID))],
		len(reg.Config.Peers),
		reg.Config.Interface.Addresses.V4)

	return &reg.Config, nil
}

// GetDeviceConfig fetches the WireGuard configuration for an existing
// device using its stored bearer token. This is the second half of the
// registration flow — used when the device was already registered in a
// previous run and we just need the peer info to rebuild the tunnel.
func (cfg *Config) GetDeviceConfig() (*warpConfig, error) {
	if cfg.DeviceID == "" || cfg.Token == "" {
		return nil, fmt.Errorf("device not registered (missing DeviceID or Token)")
	}

	getURL := fmt.Sprintf("%s/%s/reg/%s", warpAPIBase, warpAPIVer, cfg.DeviceID)
	req, _ := http.NewRequest(http.MethodGet, getURL, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.3-1922")

	resp, err := warpHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get device request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get device API returned %d: %s", resp.StatusCode, truncate(string(respBody), 400))
	}

	log.Printf("[WARP] GetDevice response (first 800 chars): %s", truncate(string(respBody), 800))

	var dev warpGetDeviceResp
	if err := json.Unmarshal(respBody, &dev); err != nil {
		return nil, fmt.Errorf("parse get device response: %w", err)
	}

	log.Printf("[WARP] Device config: peers=%d interface.v4=%s",
		len(dev.Config.Peers), dev.Config.Interface.Addresses.V4)

	return &dev.Config, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// tls12Config returns a tls.Config that forces TLS 1.2 only, matching
// the 1.1.1.1 Android app. The WARP API rejects TLS 1.3 connections
// with a 403 / error 1020.
func tls12Config() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
	}
}
