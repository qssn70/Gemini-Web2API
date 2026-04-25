package service

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	keepRecentMessages   = 8
	maxSummaryChars      = 6000
	maxPerMessageChars   = 320
)

type Message struct {
	Role    string
	Content string
}

func CompactMessages(messages []Message, maxChars int) ([]Message, bool) {
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Content) + utf8.RuneCountInString(m.Role) + 10
	}

	if total <= maxChars {
		return messages, false
	}

	if len(messages) <= keepRecentMessages {
		return messages, false
	}

	oldMessages := messages[:len(messages)-keepRecentMessages]
	recentMessages := messages[len(messages)-keepRecentMessages:]

	var summaryBuilder strings.Builder
	summaryBuilder.WriteString("[Earlier conversation summary]\n")

	summaryLen := 0
	for _, m := range oldMessages {
		content := m.Content
		runes := []rune(content)
		if len(runes) > maxPerMessageChars {
			content = string(runes[:maxPerMessageChars]) + "..."
		}

		line := fmt.Sprintf("- %s: %s\n", m.Role, content)
		lineLen := utf8.RuneCountInString(line)

		if summaryLen+lineLen > maxSummaryChars {
			break
		}
		summaryBuilder.WriteString(line)
		summaryLen += lineLen
	}

	summaryBuilder.WriteString("[/Earlier conversation summary]")

	result := make([]Message, 0, 1+len(recentMessages))
	result = append(result, Message{
		Role:    "system",
		Content: summaryBuilder.String(),
	})
	result = append(result, recentMessages...)

	return result, true
}

func BuildPromptFromMessages(messages []Message) string {
	var builder strings.Builder

	for _, msg := range messages {
		role := "User"
		switch strings.ToLower(msg.Role) {
		case "model", "assistant":
			role = "Model"
		case "system":
			role = "System"
		}

		builder.WriteString(fmt.Sprintf("**%s**: %s\n\n", role, msg.Content))
	}

	result := builder.String()
	if result == "" {
		return "Hello"
	}
	return result
}

func EstimatePromptSize(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Content) + utf8.RuneCountInString(m.Role) + 10
	}
	return total
}
