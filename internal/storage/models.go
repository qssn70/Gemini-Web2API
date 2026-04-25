package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type SessionRecord struct {
	AccountID    string    `json:"account_id"`
	CID          string    `json:"cid"`
	RID          string    `json:"rid"`
	RCID         string    `json:"rcid"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
}

func (s *SessionRecord) Marshal() ([]byte, error) {
	return json.Marshal(s)
}

func UnmarshalSession(data []byte) (*SessionRecord, error) {
	var s SessionRecord
	err := json.Unmarshal(data, &s)
	return &s, err
}

func (s *SessionRecord) IsExpired(ttlMinutes int) bool {
	return time.Since(s.LastUsedAt) > time.Duration(ttlMinutes)*time.Minute
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func HashMessages(messages []ChatMessage) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func HashMessagesPrefix(messages []ChatMessage, count int) string {
	if count > len(messages) {
		count = len(messages)
	}
	return HashMessages(messages[:count])
}

func NormalizeContent(content string) string {
	content = strings.TrimSpace(content)
	content = strings.Join(strings.Fields(content), " ")
	return strings.ToLower(content)
}

func HashMessagesNormalized(messages []ChatMessage) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(NormalizeContent(m.Content)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
