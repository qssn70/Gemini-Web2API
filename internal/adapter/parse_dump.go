package adapter

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"gemini-web2api/internal/config"
)

// dumpCounter disambiguates files written within the same millisecond when a
// burst of failures lands together (e.g. several requests in a streaming
// session all hit a stale schema before the user notices and stops).
var dumpCounter uint64

// debugDumpDir is the directory where unrecognised Gemini response bodies are
// captured for offline inspection. We co-locate it with the storage path so a
// single STORAGE_PATH points at one volume containing sessions, cookies and
// debug dumps. Returns "" if the directory cannot be created — callers treat
// that as "dumping disabled" and just include the parse counters in logs.
func debugDumpDir() string {
	cfg := config.GetConfig()
	base := "./data"
	if cfg != nil && cfg.Storage.Path != "" {
		base = filepath.Dir(cfg.Storage.Path)
	}
	dir := filepath.Join(base, "debug")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	return dir
}

// dumpUnrecognisedBody writes a raw Gemini response body to the debug dir
// when the parser failed to identify any candidate set. The path is returned
// so the caller can surface it in the error message — that filename is what
// the user copies back to us when reporting "malformed Gemini response".
//
// If body is empty, no file is written and an empty string is returned.
//
// Tag is a short label baked into the filename to distinguish the calling
// site (e.g. "stream" vs "non-stream") in case a future bug only manifests
// on one path.
func dumpUnrecognisedBody(tag string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	dir := debugDumpDir()
	if dir == "" {
		return ""
	}

	// Best-effort housekeeping: if the dump directory has grown past 50
	// files, drop the oldest. This keeps a noisy production server from
	// running out of disk if a schema change makes every request fail.
	pruneOldDumps(dir, 50)

	safeTag := sanitiseTag(tag)
	if safeTag == "" {
		safeTag = "parse"
	}
	stamp := time.Now().UTC().Format("20060102T150405.000Z")
	seq := atomic.AddUint64(&dumpCounter, 1)
	name := fmt.Sprintf("parse-fail-%s-%s-%04d.bin", stamp, safeTag, seq%10000)
	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, body, 0600); err != nil {
		log.Printf("[ParseDump] Failed to write %s: %v", path, err)
		return ""
	}
	return path
}

func sanitiseTag(tag string) string {
	out := make([]byte, 0, len(tag))
	for i := 0; i < len(tag) && i < 24; i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// pruneOldDumps deletes the oldest files in dir when the count exceeds keep.
// Errors are intentionally swallowed — this is housekeeping, not critical.
func pruneOldDumps(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		path    string
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			path:    filepath.Join(dir, e.Name()),
			modTime: info.ModTime(),
		})
	}
	if len(files) <= keep {
		return
	}
	// Sort ascending by modTime (oldest first).
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j-1].modTime.After(files[j].modTime); j-- {
			files[j-1], files[j] = files[j], files[j-1]
		}
	}
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(f.path)
	}
}

// previewBytes returns a short ASCII-safe preview of body for log lines so
// operators can eyeball what came back without opening the dump file.
func previewBytes(body []byte, limit int) string {
	if len(body) > limit {
		body = body[:limit]
	}
	// Replace control bytes (other than \n / \r / \t) with '?' to avoid
	// terminal escape injection from a malicious response body.
	out := make([]byte, len(body))
	for i, c := range body {
		switch {
		case c == '\n', c == '\r', c == '\t':
			out[i] = ' '
		case c < 0x20 || c == 0x7f:
			out[i] = '?'
		default:
			out[i] = c
		}
	}
	preview := string(bytes.TrimSpace(out))
	if len(preview) > limit {
		preview = preview[:limit] + "..."
	}
	return preview
}
