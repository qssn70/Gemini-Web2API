package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	sessionsBucket = []byte("sessions")
	indexBucket    = []byte("session_index")
)

type Store struct {
	db   *bolt.DB
	mu   sync.RWMutex
	path string
}

func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(sessionsBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(indexBucket); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create buckets: %w", err)
	}

	return &Store{db: db, path: dbPath}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) SaveSession(messagesHash string, record *SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		data, err := record.Marshal()
		if err != nil {
			return err
		}
		if err := b.Put([]byte(messagesHash), data); err != nil {
			return err
		}

		idx := tx.Bucket(indexBucket)
		indexKey := []byte(record.AccountID + ":" + record.CID)
		var hashes []string
		existing := idx.Get(indexKey)
		if existing != nil {
			_ = json.Unmarshal(existing, &hashes)
		}

		found := false
		for _, h := range hashes {
			if h == messagesHash {
				found = true
				break
			}
		}
		if !found {
			hashes = append(hashes, messagesHash)
		}

		hashData, _ := json.Marshal(hashes)
		return idx.Put(indexKey, hashData)
	})
}

func (s *Store) FindSession(messagesHash string, ttlMinutes int) (*SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var record *SessionRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		data := b.Get([]byte(messagesHash))
		if data == nil {
			return nil
		}
		r, err := UnmarshalSession(data)
		if err != nil {
			return nil
		}
		if r.IsExpired(ttlMinutes) {
			return nil
		}
		record = r
		return nil
	})
	return record, err
}

func (s *Store) FindPrefixSession(messages []ChatMessage, ttlMinutes int) (*SessionRecord, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var bestRecord *SessionRecord
	bestCount := 0

	for i := len(messages) - 1; i >= 1; i-- {
		hash := HashMessagesPrefix(messages, i)
		var record *SessionRecord
		_ = s.db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket(sessionsBucket)
			data := b.Get([]byte(hash))
			if data == nil {
				return nil
			}
			r, err := UnmarshalSession(data)
			if err != nil {
				return nil
			}
			if !r.IsExpired(ttlMinutes) {
				record = r
			}
			return nil
		})

		if record != nil {
			bestRecord = record
			bestCount = i
			break
		}
	}

	return bestRecord, bestCount, nil
}

func (s *Store) DeleteSession(messagesHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		data := b.Get([]byte(messagesHash))
		if data != nil {
			record, err := UnmarshalSession(data)
			if err == nil {
				idx := tx.Bucket(indexBucket)
				indexKey := []byte(record.AccountID + ":" + record.CID)
				var hashes []string
				existing := idx.Get(indexKey)
				if existing != nil {
					_ = json.Unmarshal(existing, &hashes)
				}
				var filtered []string
				for _, h := range hashes {
					if h != messagesHash {
						filtered = append(filtered, h)
					}
				}
				if len(filtered) == 0 {
					_ = idx.Delete(indexKey)
				} else {
					hashData, _ := json.Marshal(filtered)
					_ = idx.Put(indexKey, hashData)
				}
			}
		}
		return b.Delete([]byte(messagesHash))
	})
}

func (s *Store) TouchSession(messagesHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		data := b.Get([]byte(messagesHash))
		if data == nil {
			return nil
		}
		record, err := UnmarshalSession(data)
		if err != nil {
			return nil
		}
		record.LastUsedAt = time.Now()
		updated, _ := record.Marshal()
		return b.Put([]byte(messagesHash), updated)
	})
}

func (s *Store) Cleanup(retentionDays int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	deleted := 0

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		idx := tx.Bucket(indexBucket)

		var toDelete [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			record, err := UnmarshalSession(v)
			if err != nil || record.LastUsedAt.Before(cutoff) {
				toDelete = append(toDelete, append([]byte{}, k...))
			}
		}

		for _, key := range toDelete {
			data := b.Get(key)
			if data != nil {
				record, err := UnmarshalSession(data)
				if err == nil {
					indexKey := []byte(record.AccountID + ":" + record.CID)
					var hashes []string
					existing := idx.Get(indexKey)
					if existing != nil {
						_ = json.Unmarshal(existing, &hashes)
					}
					var filtered []string
					for _, h := range hashes {
						if h != string(key) {
							filtered = append(filtered, h)
						}
					}
					if len(filtered) == 0 {
						_ = idx.Delete(indexKey)
					} else {
						hashData, _ := json.Marshal(filtered)
						_ = idx.Put(indexKey, hashData)
					}
				}
			}
			_ = b.Delete(key)
			deleted++
		}
		return nil
	})

	return deleted, err
}

func (s *Store) StartCleanupRoutine(retentionDays, intervalHours int) {
	go func() {
		ticker := time.NewTicker(time.Duration(intervalHours) * time.Hour)
		defer ticker.Stop()

		for range ticker.C {
			count, err := s.Cleanup(retentionDays)
			if err != nil {
				log.Printf("[Storage] Cleanup error: %v", err)
			} else if count > 0 {
				log.Printf("[Storage] Cleaned up %d expired session(s)", count)
			}
		}
	}()
}

func (s *Store) Stats() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionsBucket)
		count = b.Stats().KeyN
		return nil
	})
	return count, err
}
