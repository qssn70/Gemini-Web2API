package balancer

import (
	"gemini-web2api/internal/gemini"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxConsecutiveFailures = 3
	cooldownDuration       = 5 * time.Minute
	maxRetries             = 2
)

type AccountEntry struct {
	Client    *gemini.Client
	AccountID string
	ProxyURL  string

	consecutiveFailures int32
	cooldownUntil       time.Time
	healthMu            sync.RWMutex
}

func (e *AccountEntry) RecordSuccess() {
	atomic.StoreInt32(&e.consecutiveFailures, 0)
	e.healthMu.Lock()
	e.cooldownUntil = time.Time{}
	e.healthMu.Unlock()
}

func (e *AccountEntry) RecordFailure() {
	failures := atomic.AddInt32(&e.consecutiveFailures, 1)
	if failures >= maxConsecutiveFailures {
		e.healthMu.Lock()
		e.cooldownUntil = time.Now().Add(cooldownDuration)
		e.healthMu.Unlock()
		log.Printf("[Balancer] Account '%s' entering cooldown after %d consecutive failures", e.displayID(), failures)
	}
}

func (e *AccountEntry) RecordAuthFailure() {
	atomic.StoreInt32(&e.consecutiveFailures, maxConsecutiveFailures)
	e.healthMu.Lock()
	e.cooldownUntil = time.Now().Add(cooldownDuration * 2)
	e.healthMu.Unlock()
	log.Printf("[Balancer] Account '%s' marked as possibly expired, entering extended cooldown", e.displayID())
}

func (e *AccountEntry) IsHealthy() bool {
	if atomic.LoadInt32(&e.consecutiveFailures) >= maxConsecutiveFailures {
		e.healthMu.RLock()
		defer e.healthMu.RUnlock()
		if time.Now().Before(e.cooldownUntil) {
			return false
		}
		atomic.StoreInt32(&e.consecutiveFailures, maxConsecutiveFailures-1)
	}
	return true
}

func (e *AccountEntry) displayID() string {
	if e.AccountID == "" {
		return "default"
	}
	return e.AccountID
}

type AccountPool struct {
	entries []*AccountEntry
	index   uint64
	mu      sync.RWMutex
}

func NewAccountPool() *AccountPool {
	return &AccountPool{
		entries: make([]*AccountEntry, 0),
	}
}

func (p *AccountPool) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = make([]*AccountEntry, 0)
	atomic.StoreUint64(&p.index, 0)
}

func (p *AccountPool) Add(client *gemini.Client, accountID string, proxyURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries, &AccountEntry{
		Client:    client,
		AccountID: accountID,
		ProxyURL:  proxyURL,
	})
}

func (p *AccountPool) Next() (*AccountEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.entries)
	if n == 0 {
		return nil, false
	}

	start := atomic.AddUint64(&p.index, 1) - 1
	for i := 0; i < n; i++ {
		entry := p.entries[(start+uint64(i))%uint64(n)]
		if entry.IsHealthy() {
			return entry, true
		}
	}

	entry := p.entries[start%uint64(n)]
	return entry, true
}

func (p *AccountPool) NextExcluding(excludeAccountID string) (*AccountEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.entries)
	if n == 0 {
		return nil, false
	}

	start := atomic.AddUint64(&p.index, 1) - 1
	for i := 0; i < n; i++ {
		entry := p.entries[(start+uint64(i))%uint64(n)]
		if entry.AccountID != excludeAccountID && entry.IsHealthy() {
			return entry, true
		}
	}

	return nil, false
}

func (p *AccountPool) GetMaxRetries() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.entries)
	if n <= 1 {
		return 0
	}
	if maxRetries > n-1 {
		return n - 1
	}
	return maxRetries
}

func (p *AccountPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}

// Snapshot returns a copy of the current entries slice so callers (e.g. the
// background __Secure-1PSIDTS rotator) can iterate without holding the pool
// lock during slow network calls. The returned slice can be mutated freely
// by the caller; the AccountEntry pointers, however, remain shared with the
// pool — that is intentional, since rotating cookies on the underlying
// gemini.Client is exactly the side-effect we want to keep.
func (p *AccountPool) Snapshot() []*AccountEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*AccountEntry, len(p.entries))
	copy(out, p.entries)
	return out
}

func (p *AccountPool) HealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	count := 0
	for _, e := range p.entries {
		if e.IsHealthy() {
			count++
		}
	}
	return count
}

func (p *AccountPool) ReplaceAccounts(newAccountIDs []string, changedEntries map[string]*AccountEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	oldEntries := make(map[string]*AccountEntry)
	for _, entry := range p.entries {
		oldEntries[entry.AccountID] = entry
	}

	p.entries = make([]*AccountEntry, 0, len(newAccountIDs))
	for _, accountID := range newAccountIDs {
		if newEntry, changed := changedEntries[accountID]; changed {
			p.entries = append(p.entries, newEntry)
		} else if oldEntry, existed := oldEntries[accountID]; existed {
			p.entries = append(p.entries, oldEntry)
		}
	}
}
