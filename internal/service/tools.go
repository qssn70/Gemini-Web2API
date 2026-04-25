package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

const toolSystemPrompt = `You have access to the following tools. When you need to call a tool, use EXACTLY this format:

[ToolCalls]
[Call:functionName]
[CallParameter:paramName]
value
[/CallParameter:paramName]
[/Call:functionName]
[/ToolCalls]

You may call multiple tools in a single response. After the tool results are returned, continue your response.

Available tools:
`

const toolRequiredSuffix = `
IMPORTANT: You MUST use at least one of the available tools in your response.`

func BuildToolPrompt(tools []Tool, toolChoice interface{}) string {
	if len(tools) == 0 {
		return ""
	}

	choiceStr := "auto"
	switch v := toolChoice.(type) {
	case string:
		choiceStr = v
	case map[string]interface{}:
		if fn, ok := v["function"].(map[string]interface{}); ok {
			if name, ok := fn["name"].(string); ok {
				choiceStr = name
			}
		}
	}

	if choiceStr == "none" {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(toolSystemPrompt)

	for _, tool := range tools {
		builder.WriteString(fmt.Sprintf("\n### %s\n", tool.Function.Name))
		if tool.Function.Description != "" {
			builder.WriteString(fmt.Sprintf("Description: %s\n", tool.Function.Description))
		}
		if tool.Function.Parameters != nil {
			params, _ := json.MarshalIndent(tool.Function.Parameters, "", "  ")
			builder.WriteString(fmt.Sprintf("Parameters: %s\n", string(params)))
		}
	}

	if choiceStr == "required" {
		builder.WriteString(toolRequiredSuffix)
	} else if choiceStr != "auto" {
		builder.WriteString(fmt.Sprintf("\nIMPORTANT: You MUST call the '%s' function.", choiceStr))
	}

	return builder.String()
}

var (
	toolBlockRe = regexp.MustCompile(`(?s)\[ToolCalls\](.*?)\[/ToolCalls\]`)
	toolCallRe  = regexp.MustCompile(`(?s)\[Call:(\w+)\](.*?)\[/Call:(\w+)\]`)
	toolParamRe = regexp.MustCompile(`(?s)\[CallParameter:(\w+)\](.*?)\[/CallParameter:(\w+)\]`)
)

func ParseToolCalls(text string) ([]ToolCall, string) {
	matches := toolBlockRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, text
	}

	var calls []ToolCall
	cleanText := text

	for blockIdx, match := range matches {
		_ = blockIdx
		blockContent := text[match[2]:match[3]]

		callMatches := toolCallRe.FindAllStringSubmatch(blockContent, -1)
		for callIdx, cm := range callMatches {
			funcName := cm[1]
			callBody := cm[2]
			endName := cm[3]
			if funcName != endName {
				continue
			}

			args := make(map[string]interface{})
			paramMatches := toolParamRe.FindAllStringSubmatch(callBody, -1)
			for _, pm := range paramMatches {
				paramName := pm[1]
				paramValue := strings.TrimSpace(pm[2])
				endParamName := pm[3]
				if paramName != endParamName {
					continue
				}

				var jsonVal interface{}
				if err := json.Unmarshal([]byte(paramValue), &jsonVal); err == nil {
					args[paramName] = jsonVal
				} else {
					args[paramName] = paramValue
				}
			}

			argsJSON, _ := json.Marshal(args)

			call := ToolCall{
				ID:   generateToolCallID(funcName, string(argsJSON), callIdx),
				Type: "function",
			}
			call.Function.Name = funcName
			call.Function.Arguments = string(argsJSON)
			calls = append(calls, call)
		}
	}

	cleanText = toolBlockRe.ReplaceAllString(text, "")
	cleanText = strings.ReplaceAll(cleanText, "\r\n", "\n")
	cleanText = strings.ReplaceAll(cleanText, "\n\n\n", "\n\n")
	cleanText = strings.TrimSpace(cleanText)
	cleanText = strings.ReplaceAll(cleanText, "\n\n", "\n")

	return calls, cleanText
}

func generateToolCallID(funcName, args string, index int) string {
	h := sha256.New()
	h.Write([]byte(funcName))
	h.Write([]byte(args))
	h.Write([]byte(fmt.Sprintf("%d", index)))
	return "call_" + hex.EncodeToString(h.Sum(nil))[:24]
}

type StreamToolFilter struct {
	buffer    strings.Builder
	inToolBlock bool
	pending   string
}

func NewStreamToolFilter() *StreamToolFilter {
	return &StreamToolFilter{}
}

func (f *StreamToolFilter) Process(chunk string) (visible string, toolCalls []ToolCall) {
	f.buffer.WriteString(chunk)
	content := f.buffer.String()

	if f.inToolBlock {
		if idx := strings.Index(content, "[/ToolCalls]"); idx >= 0 {
			blockEnd := idx + len("[/ToolCalls]")
			blockContent := content[:blockEnd]
			calls, _ := ParseToolCalls(blockContent)
			toolCalls = calls
			f.buffer.Reset()
			remaining := content[blockEnd:]
			f.buffer.WriteString(remaining)
			f.inToolBlock = false
			return "", toolCalls
		}
		return "", nil
	}

	if idx := strings.Index(content, "[ToolCalls]"); idx >= 0 {
		visible = content[:idx]
		f.buffer.Reset()
		f.buffer.WriteString(content[idx:])
		f.inToolBlock = true
		return strings.TrimSpace(visible), nil
	}

	if strings.HasSuffix(content, "[") ||
		strings.HasSuffix(content, "[T") ||
		strings.HasSuffix(content, "[To") ||
		strings.HasSuffix(content, "[Too") ||
		strings.HasSuffix(content, "[Tool") ||
		strings.HasSuffix(content, "[ToolC") ||
		strings.HasSuffix(content, "[ToolCa") ||
		strings.HasSuffix(content, "[ToolCal") ||
		strings.HasSuffix(content, "[ToolCall") ||
		strings.HasSuffix(content, "[ToolCalls") {
		safeEnd := strings.LastIndex(content, "[")
		if safeEnd > 0 {
			visible = content[:safeEnd]
			f.buffer.Reset()
			f.buffer.WriteString(content[safeEnd:])
			return visible, nil
		}
		return "", nil
	}

	f.buffer.Reset()
	return content, nil
}

func (f *StreamToolFilter) Flush() (string, []ToolCall) {
	content := f.buffer.String()
	f.buffer.Reset()

	if f.inToolBlock {
		calls, clean := ParseToolCalls(content)
		f.inToolBlock = false
		return clean, calls
	}

	return content, nil
}
