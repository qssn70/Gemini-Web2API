package service

import (
	"fmt"
	"gemini-web2api/internal/balancer"
	"gemini-web2api/internal/config"
	"gemini-web2api/internal/gemini"
	"gemini-web2api/internal/storage"
	"io"
	"log"
	"strings"
	"time"
)

type ChatService struct {
	Pool  *balancer.AccountPool
	Store *storage.Store
}

type ChatRequest struct {
	Messages []Message
	Model    string
	Files    []gemini.FileData
}

type ChatResult struct {
	Body      io.ReadCloser
	Entry     *balancer.AccountEntry
	SessionID string
	Meta      *gemini.ChatMetadata
}

func NewChatService(pool *balancer.AccountPool, store *storage.Store) *ChatService {
	return &ChatService{
		Pool:  pool,
		Store: store,
	}
}

func (s *ChatService) Send(req *ChatRequest, entry *balancer.AccountEntry) (*ChatResult, error) {
	cfg := config.GetConfig()
	messages := req.Messages

	maxChars := config.GetMaxChars()
	if EstimatePromptSize(messages) > maxChars {
		compacted, did := CompactMessages(messages, maxChars)
		if did {
			log.Printf("[ChatService] Compacted %d messages to %d", len(messages), len(compacted))
			messages = compacted
		}
	}

	var meta *gemini.ChatMetadata
	var sessionHash string

	if s.Store != nil && !config.IsTemporaryChat() {
		storeMsgs := messagesToStorageFormat(messages)
		fullHash := storage.HashMessages(storeMsgs)

		record, err := s.Store.FindSession(fullHash, cfg.Gemini.SessionTTLMinutes)
		if err == nil && record != nil {
			meta = &gemini.ChatMetadata{
				CID:  record.CID,
				RID:  record.RID,
				RCID: record.RCID,
			}
			sessionHash = fullHash
			log.Printf("[ChatService] Reusing session CID=%s (exact match)", record.CID[:8])
		}

		if meta == nil {
			record, matchCount, err := s.Store.FindPrefixSession(storeMsgs, cfg.Gemini.SessionTTLMinutes)
			if err == nil && record != nil {
				meta = &gemini.ChatMetadata{
					CID:  record.CID,
					RID:  record.RID,
					RCID: record.RCID,
				}
				messages = messages[matchCount:]
				sessionHash = storage.HashMessagesPrefix(storeMsgs, matchCount)
				log.Printf("[ChatService] Reusing session CID=%s (prefix match, sending %d new messages)", record.CID[:8], len(messages))
			}
		}
	}

	prompt := BuildPromptFromMessages(messages)

	gemini.RandomDelay()

	body, err := entry.Client.StreamGenerateContent(prompt, req.Model, req.Files, meta)
	if err != nil {
		if meta != nil && sessionHash != "" {
			log.Printf("[ChatService] Session reuse failed, retrying with fresh session: %v", err)
			if s.Store != nil {
				s.Store.DeleteSession(sessionHash)
			}
			meta = nil
			prompt = BuildPromptFromMessages(req.Messages)
			body, err = entry.Client.StreamGenerateContent(prompt, req.Model, req.Files, nil)
			if err != nil {
				entry.RecordFailure()
				return nil, err
			}
		} else {
			entry.RecordFailure()

			if gemini.IsAuthError(err) {
				entry.RecordAuthFailure()
			}

			return nil, err
		}
	}

	entry.RecordSuccess()

	return &ChatResult{
		Body:      body,
		Entry:     entry,
		SessionID: sessionHash,
		Meta:      meta,
	}, nil
}

func (s *ChatService) SendWithRetry(req *ChatRequest) (*ChatResult, error) {
	entry, ok := s.Pool.Next()
	if !ok || entry == nil {
		return nil, fmt.Errorf("no available accounts")
	}

	result, err := s.Send(req, entry)
	if err == nil {
		return result, nil
	}

	maxRetries := s.Pool.GetMaxRetries()
	excludeID := entry.AccountID
	var lastErr error = err

	for i := 0; i < maxRetries; i++ {
		retryEntry, ok := s.Pool.NextExcluding(excludeID)
		if !ok || retryEntry == nil {
			break
		}

		log.Printf("[ChatService] Retrying on account '%s' (attempt %d/%d)", retryEntry.AccountID, i+1, maxRetries)

		result, err := s.Send(req, retryEntry)
		if err == nil {
			return result, nil
		}

		lastErr = err
		excludeID = retryEntry.AccountID
	}

	return nil, fmt.Errorf("all accounts failed: %v", lastErr)
}

func (s *ChatService) SaveSession(messages []Message, meta *gemini.ChatMetadata, accountID string) {
	if s.Store == nil || config.IsTemporaryChat() || meta == nil {
		return
	}

	storeMsgs := messagesToStorageFormat(messages)
	hash := storage.HashMessages(storeMsgs)

	record := &storage.SessionRecord{
		AccountID:    accountID,
		CID:          meta.CID,
		RID:          meta.RID,
		RCID:         meta.RCID,
		MessageCount: len(messages),
		CreatedAt:    time.Now(),
		LastUsedAt:   time.Now(),
	}

	if err := s.Store.SaveSession(hash, record); err != nil {
		log.Printf("[ChatService] Failed to save session: %v", err)
	}
}

func messagesToStorageFormat(messages []Message) []storage.ChatMessage {
	result := make([]storage.ChatMessage, len(messages))
	for i, m := range messages {
		result[i] = storage.ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return result
}

func ExtractSessionMetadata(responseText string) *gemini.ChatMetadata {
	// Gemini web responses include conversation metadata in the response body.
	// The CID/RID/RCID are at specific JSON paths in the response.
	// This is parsed during streaming; for now we extract from the full response.
	// TODO: Extract from streaming response chunks as they arrive
	lines := strings.Split(responseText, "\n")
	for _, line := range lines {
		line = strings.TrimPrefix(line, ")]}'")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Look for conversation metadata pattern
		// In the Gemini response, the CID is typically at path [0][2] of the inner data
		// This is a simplification; real extraction happens in parseGeminiResponse
		_ = line
	}
	return nil
}
