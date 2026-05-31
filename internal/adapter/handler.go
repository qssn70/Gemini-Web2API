package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gemini-web2api/internal/balancer"
	"gemini-web2api/internal/config"
	"gemini-web2api/internal/gemini"
	"gemini-web2api/internal/service"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ChatRequest struct {
	Messages   []ChatMessage  `json:"messages"`
	Stream     bool           `json:"stream"`
	Model      string         `json:"model"`
	Tools      []service.Tool `json:"tools,omitempty"`
	ToolChoice interface{}    `json:"tool_choice,omitempty"`
}

func ListModelsHandler(c *gin.Context) {
	type ModelCard struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	models := []ModelCard{
		{ID: "gemini-2.5-flash", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3.1-pro-preview", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-flash-preview", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-flash-preview-no-thinking", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-2.5-flash-image", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-pro-image-preview", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-pro", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-flash", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
		{ID: "gemini-3-flash-thinking", Object: "model", Created: time.Now().Unix(), OwnedBy: "Google"},
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

func isImageModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "image")
}

func ChatCompletionHandler(pool *balancer.AccountPool) gin.HandlerFunc {
	return func(c *gin.Context) {
		entry, ok := pool.Next()
		if !ok || entry == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No available accounts"})
			return
		}
		client := entry.Client

		c.Set("account_id", entry.AccountID)

		var req ChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		mappedModel := config.MapModel(req.Model)
		if mappedModel != req.Model {
			log.Printf("[OpenAI] Model mapped: %s -> %s", req.Model, mappedModel)
		}

		if isImageModel(mappedModel) {
			handleImageChatRequest(c, client, req)
			return
		}

		var promptBuilder strings.Builder
		var files []gemini.FileData

		toolPrompt := service.BuildToolPrompt(req.Tools, req.ToolChoice)
		hasTools := toolPrompt != ""

		for _, msg := range req.Messages {
			role := "User"
			if strings.EqualFold(msg.Role, "model") || strings.EqualFold(msg.Role, "assistant") {
				role = "Model"
			} else if strings.EqualFold(msg.Role, "system") {
				role = "System"
			} else if strings.EqualFold(msg.Role, "tool") {
				role = "User"
			}

			promptBuilder.WriteString(fmt.Sprintf("**%s**: ", role))

			switch v := msg.Content.(type) {
			case string:
				promptBuilder.WriteString(v)
			case []interface{}:
				for _, part := range v {
					p, ok := part.(map[string]interface{})
					if !ok {
						continue
					}

					typeStr, _ := p["type"].(string)

					if typeStr == "text" {
						if text, ok := p["text"].(string); ok {
							promptBuilder.WriteString(text)
						}
					} else if typeStr == "image_url" {
						if imgMap, ok := p["image_url"].(map[string]interface{}); ok {
							if urlStr, ok := imgMap["url"].(string); ok {
								if strings.HasPrefix(urlStr, "data:") {
									parts := strings.Split(urlStr, ",")
									if len(parts) == 2 {
										data, err := base64.StdEncoding.DecodeString(parts[1])
										if err == nil {
											fname := fmt.Sprintf("image_%d.png", time.Now().UnixNano())
											fid, err := client.UploadFile(data, fname)
											if err == nil {
												files = append(files, gemini.FileData{
													URL:      fid,
													FileName: fname,
												})
												promptBuilder.WriteString("[Image]")
											} else {
												log.Printf("Failed to upload image: %v", err)
											}
										}
									}
								} else if strings.HasPrefix(urlStr, "http") {
									fd, err := client.DownloadAndUpload(urlStr)
									if err == nil {
										files = append(files, fd)
										promptBuilder.WriteString("[Image]")
									} else {
										log.Printf("Failed to download image from URL: %v", err)
										promptBuilder.WriteString(fmt.Sprintf("[Image URL: %s]", urlStr))
									}
								} else {
									promptBuilder.WriteString(fmt.Sprintf("[Image URL: %s]", urlStr))
								}
							}
						}
					} else if typeStr == "file" {
						if fileMap, ok := p["file"].(map[string]interface{}); ok {
							if dataStr, ok := fileMap["data"].(string); ok {
								mimeType, _ := fileMap["mime_type"].(string)
								fname, _ := fileMap["filename"].(string)
								if fname == "" {
									fname = fmt.Sprintf("file_%d", time.Now().UnixNano())
									if ext := mimeTypeToFileExt(mimeType); ext != "" {
										fname += ext
									}
								}
								data, err := base64.StdEncoding.DecodeString(dataStr)
								if err == nil {
									fid, err := client.UploadFile(data, fname)
									if err == nil {
										files = append(files, gemini.FileData{URL: fid, FileName: fname})
										promptBuilder.WriteString(fmt.Sprintf("[File: %s]", fname))
									} else {
										log.Printf("Failed to upload file: %v", err)
									}
								}
							} else if urlStr, ok := fileMap["url"].(string); ok {
								fd, err := client.DownloadAndUpload(urlStr)
								if err == nil {
									files = append(files, fd)
									promptBuilder.WriteString(fmt.Sprintf("[File: %s]", fd.FileName))
								} else {
									log.Printf("Failed to download file from URL: %v", err)
								}
							}
						}
					}
				}
			}
			promptBuilder.WriteString("\n\n")
		}

		if hasTools {
			promptBuilder.WriteString("\n" + toolPrompt + "\n")
		}

		finalPrompt := promptBuilder.String()
		if finalPrompt == "" {
			finalPrompt = "Hello"
		}

		gemini.RandomDelay()

		respBody, err := client.StreamGenerateContent(finalPrompt, mappedModel, files, nil)
		if err != nil {
			log.Printf("Gemini request failed: %v", err)

			if gemini.IsAuthError(err) {
				entry.RecordAuthFailure()
			} else {
				entry.RecordFailure()
			}

			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to communicate with Gemini: " + err.Error()})
			return
		}
		defer respBody.Close()

		entry.RecordSuccess()

		id := fmt.Sprintf("chatcmpl-%d", time.Now().Unix())
		created := time.Now().Unix()

		if !req.Stream {
			var fullText strings.Builder
			var fullThinking strings.Builder

			parseStatus, parseErr := parseGeminiResponse(respBody, func(text, thought string) {
				fullText.WriteString(text)
				fullThinking.WriteString(thought)
			})
			if err := geminiParseError(parseStatus, parseErr); err != nil {
				log.Printf("[OpenAI] Failed to parse Gemini response: %v", err)
				respondWithGeminiError(c, err)
				return
			}

			responseText := fullText.String()
			message := map[string]interface{}{
				"role":              "assistant",
				"content":           responseText,
				"reasoning_content": fullThinking.String(),
			}

			finishReason := "stop"
			if hasTools {
				toolCalls, cleanText := service.ParseToolCalls(responseText)
				if len(toolCalls) > 0 {
					message["content"] = cleanText
					message["tool_calls"] = toolCalls
					finishReason = "tool_calls"
				}
			}

			resp := map[string]interface{}{
				"id":      id,
				"object":  "chat.completion",
				"created": created,
				"model":   req.Model,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"message":       message,
						"finish_reason": finishReason,
					},
				},
			}
			c.JSON(http.StatusOK, resp)
			return
		}

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Transfer-Encoding", "chunked")

		sendSSERole(c.Writer, id, created, req.Model)

		if hasTools {
			toolFilter := service.NewStreamToolFilter()

			c.Stream(func(w io.Writer) bool {
				parseStatus, parseErr := parseGeminiResponse(respBody, func(text, thought string) {
					if thought != "" {
						sendSSEThinking(w, id, created, req.Model, thought)
					}
					if text != "" {
						visible, toolCalls := toolFilter.Process(text)
						if visible != "" {
							sendSSE(w, id, created, req.Model, visible)
						}
						if len(toolCalls) > 0 {
							sendSSEToolCalls(w, id, created, req.Model, toolCalls)
						}
					}
				})
				if err := geminiParseError(parseStatus, parseErr); err != nil {
					log.Printf("[OpenAI] Failed to parse Gemini stream: %v", err)
					writeOpenAIStreamError(w, id, created, req.Model, err)
				}

				remaining, toolCalls := toolFilter.Flush()
				if remaining != "" {
					sendSSE(w, id, created, req.Model, remaining)
				}
				if len(toolCalls) > 0 {
					sendSSEToolCalls(w, id, created, req.Model, toolCalls)
				}

				return false
			})
		} else {
			c.Stream(func(w io.Writer) bool {
				parseStatus, parseErr := parseGeminiResponse(respBody, func(text, thought string) {
					if thought != "" {
						sendSSEThinking(w, id, created, req.Model, thought)
					}
					if text != "" {
						sendSSE(w, id, created, req.Model, text)
					}
				})
				if err := geminiParseError(parseStatus, parseErr); err != nil {
					log.Printf("[OpenAI] Failed to parse Gemini stream: %v", err)
					writeOpenAIStreamError(w, id, created, req.Model, err)
				}
				return false
			})
		}

		w := c.Writer
		fmt.Fprintf(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}
}

type geminiParseStatus struct {
	Recognized   bool
	Emitted      bool
	FrameCount   int
	PartCount    int
	PayloadCount int
	CandidateSet int
	MatchedNodes int
	// DumpPath is set when the parser could not recognise the response and a
	// raw copy was written to disk for offline inspection. Empty when the
	// parse succeeded or when dumping was disabled / failed.
	DumpPath string
	// Preview is a short ASCII-safe excerpt of the raw body, included in
	// error messages so an operator can spot the response type at a glance
	// (e.g. an HTML login page) without opening the dump file.
	Preview string
}

func geminiParseError(status geminiParseStatus, err error) error {
	if err != nil {
		// A typed BardError surfaces from the parser when the upstream
		// returned a structured error envelope (1060 IP block, 1037 quota,
		// etc.) instead of candidate content. Pass it through unchanged so
		// the handler can map it to an appropriate HTTP status code.
		if _, ok := gemini.IsBardError(err); ok {
			return err
		}
		return fmt.Errorf("failed to read Gemini response stream: %w", err)
	}
	if !status.Recognized {
		base := fmt.Sprintf("malformed Gemini response: no supported output structure found (frames=%d parts=%d payloads=%d candidate_sets=%d matched_nodes=%d)",
			status.FrameCount, status.PartCount, status.PayloadCount, status.CandidateSet, status.MatchedNodes)
		if status.DumpPath != "" {
			base += fmt.Sprintf("; raw body saved to %s", status.DumpPath)
		}
		if status.Preview != "" {
			base += fmt.Sprintf("; preview: %s", status.Preview)
		}
		return fmt.Errorf("%s", base)
	}
	return nil
}

// geminiErrorStatus picks the HTTP status code that best matches err. Used
// by the per-protocol handlers (OpenAI/Claude/Gemini/Responses) so they can
// stop blanket-502'ing every parse failure.
//
// Mapping rationale:
//   - 1037 USAGE_LIMIT_EXCEEDED → 429 (client should back off / try later)
//   - 1060 IP_TEMPORARILY_BLOCKED → 429 (same: retryable after wait)
//   - 1050/1052 MODEL_* → 400 (request-level fault, model name wrong)
//   - 1013 TEMPORARY_ERROR_1013 → 502 (upstream-side, retryable later)
//   - other / non-BardError → 502 (we can't tell, treat as upstream)
func geminiErrorStatus(err error) int {
	be, ok := gemini.IsBardError(err)
	if !ok {
		return http.StatusBadGateway
	}
	switch be.Code {
	case gemini.BardErrUsageLimit, gemini.BardErrIPBlocked:
		return http.StatusTooManyRequests
	case gemini.BardErrModelInconsistent, gemini.BardErrModelHeaderBad:
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

// geminiErrorType returns the OpenAI/Claude-style error.type string for err.
// "gemini_api_error" is reserved for typed BardError responses so clients
// can distinguish a structured upstream error from a parse miss.
func geminiErrorType(err error) string {
	if _, ok := gemini.IsBardError(err); ok {
		return "gemini_api_error"
	}
	return "upstream_parse_error"
}

// respondWithGeminiError writes a JSON error body shaped like OpenAI's
// {"error": {...}} envelope with a status code derived from err. Used
// by the OpenAI-compatible, Responses, and Gemini-native non-stream paths.
func respondWithGeminiError(c *gin.Context, err error) {
	body := gin.H{"error": gin.H{
		"message": err.Error(),
		"type":    geminiErrorType(err),
	}}
	if be, ok := gemini.IsBardError(err); ok {
		body["error"].(gin.H)["code"] = be.Code
	}
	c.JSON(geminiErrorStatus(err), body)
}

// respondWithClaudeError mirrors respondWithGeminiError but with the
// Anthropic Messages-API error envelope shape: {"type":"error","error":{...}}.
func respondWithClaudeError(c *gin.Context, err error) {
	inner := gin.H{
		"type":    geminiErrorType(err),
		"message": err.Error(),
	}
	if be, ok := gemini.IsBardError(err); ok {
		inner["code"] = be.Code
	}
	c.JSON(geminiErrorStatus(err), gin.H{
		"type":  "error",
		"error": inner,
	})
}

// Extract common parsing logic
func parseGeminiResponse(reader io.Reader, onChunk func(text, thought string)) (geminiParseStatus, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return geminiParseStatus{}, err
	}
	parts := extractGeminiResponseParts(string(body))
	var lastText, lastThoughts string
	status := geminiParseStatus{FrameCount: len(extractGeminiResponseFrames(string(body))), PartCount: len(parts)}

	// First sweep: scan parts for an embedded BardErrorInfo envelope. Google
	// uses these to convey upstream errors (1037 usage limit, 1060 IP block,
	// 1013 transient, etc.) inline with normal frames; if we find one we
	// short-circuit with a typed error so the handler can produce a
	// useful HTTP status code instead of "malformed Gemini response".
	//
	// Path comes from upstream Python: part[5][2][0][1][0]. Example:
	//   ["wrb.fr",null,null,null,null,
	//     [9,null,
	//       [["type.googleapis.com/.../BardErrorInfo", [1060]]]]]
	for _, part := range parts {
		errCode := extractBardErrorCode(part)
		if errCode != 0 {
			return status, gemini.NewBardError(errCode)
		}
	}

	for _, part := range parts {
		payloads := extractGeminiPayloadStrings(part)
		status.PayloadCount += len(payloads)
		for _, dataStr := range payloads {
			inner := gjson.Parse(dataStr)
			candidateSets := extractGeminiCandidateSets(inner)
			status.CandidateSet += len(candidateSets)
			status.MatchedNodes += countGeminiCandidateLikeNodes(inner)

			for _, candidates := range candidateSets {
				candidates.ForEach(func(_, candidate gjson.Result) bool {
					rawText := ""
					rawThoughts := ""

					parts := candidate.Get("1.1")
					if parts.IsArray() {
						status.Recognized = true
						parts.ForEach(func(_, part gjson.Result) bool {
							if !part.IsArray() {
								return true
							}

							textValue := part.Get("0")
							if textValue.Type != gjson.String {
								return true
							}
							text := textValue.String()
							if text == "" {
								return true
							}

							isThought := false
							if part.Get("2").Exists() {
								isThought = part.Get("2").Bool()
							}

							if isThought {
								rawThoughts += text
							} else {
								rawText += text
							}
							return true
						})
					}

					if rawText == "" {
						fallbackText := candidate.Get("1.0")
						if fallbackText.Type == gjson.String {
							status.Recognized = true
							rawText = fallbackText.String()
						}
					}
					if rawText == "" {
						cardText := candidate.Get("22.0")
						if cardText.Type == gjson.String {
							status.Recognized = true
							rawText = cardText.String()
						}
					}
					if rawThoughts == "" {
						fallbackThoughts := candidate.Get("37.0.0")
						if fallbackThoughts.Type == gjson.String {
							status.Recognized = true
							rawThoughts = fallbackThoughts.String()
						}
					}

					deltaText := ""
					deltaThoughts := ""

					rawRunes := []rune(rawText)
					lastRunes := []rune(lastText)
					if len(rawRunes) > len(lastRunes) {
						deltaText = string(rawRunes[len(lastRunes):])
						lastText = rawText
					} else if len(lastRunes) == 0 && len(rawRunes) > 0 {
						deltaText = rawText
						lastText = rawText
					}

					rawThoughtRunes := []rune(rawThoughts)
					lastThoughtRunes := []rune(lastThoughts)
					if len(rawThoughtRunes) > len(lastThoughtRunes) {
						deltaThoughts = string(rawThoughtRunes[len(lastThoughtRunes):])
						lastThoughts = rawThoughts
					} else if len(lastThoughtRunes) == 0 && len(rawThoughtRunes) > 0 {
						deltaThoughts = rawThoughts
						lastThoughts = rawThoughts
					}

					if deltaText == "" && deltaThoughts == "" {
						return true
					}

					deltaText = strings.ReplaceAll(deltaText, `\<`, `<`)
					deltaText = strings.ReplaceAll(deltaText, `\>`, `>`)
					deltaText = strings.ReplaceAll(deltaText, `\_`, `_`)
					deltaText = strings.ReplaceAll(deltaText, `\[`, `[`)
					deltaText = strings.ReplaceAll(deltaText, `\]`, `]`)
					deltaText = filterImagePlaceholders(deltaText)

					if deltaText != "" || deltaThoughts != "" {
						status.Emitted = true
						onChunk(deltaText, deltaThoughts)
					}
					return true
				})
			}
		}
	}

	// If the parser walked the whole response without finding a structure
	// it understands, persist the raw body for offline schema diagnosis.
	// This is the bridge that lets us debug Google-side schema changes
	// without asking the operator to recompile with extra logging.
	if !status.Recognized {
		status.DumpPath = dumpUnrecognisedBody("stream", body)
		status.Preview = previewBytes(body, 240)
		log.Printf("[ParseDump] Unrecognised Gemini response: frames=%d parts=%d payloads=%d candidate_sets=%d matched_nodes=%d; dump=%s; preview=%s",
			status.FrameCount, status.PartCount, status.PayloadCount, status.CandidateSet, status.MatchedNodes,
			status.DumpPath, status.Preview)
	}

	return status, nil
}

// extractBardErrorCode pulls a BardErrorInfo numeric code out of a parsed
// frame, or returns 0 if no error envelope is present. The lookup follows
// the upstream-Python convention: part[5][2][0][1][0] holds the code, and
// part[5][2][0][0] holds the type URL we use to confirm we matched the
// right envelope (rather than some unrelated array shape).
//
// Example matched part:
//
//	["wrb.fr", null, null, null, null,
//	  [9, null,
//	    [["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",
//	      [1060]]]]]
func extractBardErrorCode(part gjson.Result) int {
	envelope := part.Get("5.2.0")
	if !envelope.Exists() || !envelope.IsArray() {
		return 0
	}
	typeURL := envelope.Get("0")
	if typeURL.Type != gjson.String || !strings.Contains(typeURL.String(), "BardErrorInfo") {
		return 0
	}
	codeArray := envelope.Get("1")
	if !codeArray.IsArray() {
		return 0
	}
	code := codeArray.Get("0")
	if code.Type != gjson.Number {
		return 0
	}
	return int(code.Int())
}

func extractGeminiCandidateSets(payload gjson.Result) []gjson.Result {
	var sets []gjson.Result
	seen := map[string]struct{}{}
	addSet := func(candidateSet gjson.Result, requireCandidateLike bool) {
		if !candidateSet.IsArray() {
			return
		}
		if requireCandidateLike && !isGeminiCandidateSet(candidateSet) {
			return
		}
		key := candidateSet.Raw
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			sets = append(sets, candidateSet)
		}
	}
	var add func(gjson.Result)
	add = func(value gjson.Result) {
		if !value.Exists() {
			return
		}
		for _, path := range []string{"4", "0.4", "1.4", "response.candidates", "candidates"} {
			addSet(value.Get(path), false)
		}

		for _, path := range []string{"0", "1", "2"} {
			nested := value.Get(path)
			if nested.Type != gjson.String {
				continue
			}
			raw := strings.TrimSpace(nested.String())
			if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{") {
				add(gjson.Parse(raw))
			}
		}
	}
	add(payload)

	visited := 0
	var walk func(gjson.Result, int)
	walk = func(value gjson.Result, depth int) {
		if depth > 6 || visited > 500 || !value.Exists() {
			return
		}
		visited++
		addSet(value, true)
		if value.IsArray() || value.IsObject() {
			value.ForEach(func(_, child gjson.Result) bool {
				walk(child, depth+1)
				return visited <= 500
			})
		}
	}
	walk(payload, 0)

	return sets
}

func isGeminiCandidateSet(value gjson.Result) bool {
	if !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(_, candidate gjson.Result) bool {
		if isGeminiCandidateLike(candidate) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isGeminiCandidateLike(candidate gjson.Result) bool {
	if !candidate.Exists() || (!candidate.IsArray() && !candidate.IsObject()) {
		return false
	}
	for _, path := range []string{"1.0", "22.0", "37.0.0"} {
		if candidate.Get(path).Type == gjson.String {
			return true
		}
	}
	for _, path := range []string{"1.1", "content.parts", "parts"} {
		if containsStringPart(candidate.Get(path)) {
			return true
		}
	}
	return false
}

func containsStringPart(value gjson.Result) bool {
	if value.Type == gjson.String {
		return true
	}
	if !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(_, part gjson.Result) bool {
		if part.Type == gjson.String || part.Get("0").Type == gjson.String || part.Get("text").Type == gjson.String {
			found = true
			return false
		}
		return true
	})
	return found
}

func countGeminiCandidateLikeNodes(payload gjson.Result) int {
	count := 0
	visited := 0
	var walk func(gjson.Result, int)
	walk = func(value gjson.Result, depth int) {
		if depth > 6 || visited > 500 || !value.Exists() {
			return
		}
		visited++
		if isGeminiCandidateLike(value) {
			count++
		}
		if value.IsArray() || value.IsObject() {
			value.ForEach(func(_, child gjson.Result) bool {
				walk(child, depth+1)
				return visited <= 500
			})
		}
	}
	walk(payload, 0)
	return count
}

func extractGeminiPayloadStrings(part gjson.Result) []string {
	var payloads []string
	for _, path := range []string{"2", "1", "0"} {
		value := part.Get(path)
		if !value.Exists() || value.Type != gjson.String {
			continue
		}
		payload := strings.TrimSpace(value.String())
		if strings.HasPrefix(payload, "[") || strings.HasPrefix(payload, "{") {
			payloads = append(payloads, payload)
		}
	}

	if part.IsArray() {
		part.ForEach(func(_, value gjson.Result) bool {
			if value.Type != gjson.String {
				return true
			}
			payload := strings.TrimSpace(value.String())
			if strings.HasPrefix(payload, "[") || strings.HasPrefix(payload, "{") {
				payloads = append(payloads, payload)
			}
			return true
		})
	}

	if strings.HasPrefix(strings.TrimSpace(part.Raw), "[") || strings.HasPrefix(strings.TrimSpace(part.Raw), "{") {
		payloads = append(payloads, part.Raw)
	}

	return payloads
}

func extractGeminiResponseParts(body string) []gjson.Result {
	frames := extractGeminiResponseFrames(body)
	var parts []gjson.Result
	for _, frame := range frames {
		parsed := gjson.Parse(frame)
		if parsed.IsArray() {
			if isGeminiRPCRow(parsed) {
				parts = append(parts, parsed)
				continue
			}
			parsed.ForEach(func(_, value gjson.Result) bool {
				parts = append(parts, value)
				return true
			})
			continue
		}
		if parsed.Exists() {
			parts = append(parts, parsed)
		}
	}
	return parts
}

func isGeminiRPCRow(value gjson.Result) bool {
	if !value.IsArray() {
		return false
	}
	first := value.Get("0")
	if !first.Exists() || first.Type != gjson.String {
		return false
	}
	tag := first.String()
	return tag == "wrb.fr" || strings.HasPrefix(tag, "wrb.")
}

func extractGeminiResponseFrames(body string) []string {
	body = strings.TrimPrefix(body, ")]}'")
	var frames []string
	remaining := strings.TrimSpace(body)

	for remaining != "" {
		remaining = strings.TrimLeft(remaining, "\r\n\t ")
		if remaining == "" {
			break
		}

		idx := 0
		for idx < len(remaining) && remaining[idx] >= '0' && remaining[idx] <= '9' {
			idx++
		}
		if idx > 0 {
			frameLen := 0
			for _, r := range remaining[:idx] {
				frameLen = frameLen*10 + int(r-'0')
			}
			rest := strings.TrimLeft(remaining[idx:], "\r\n")
			if frameLen > 0 && len(rest) >= frameLen {
				frame := strings.TrimSpace(rest[:frameLen])
				if strings.HasPrefix(frame, "[") || strings.HasPrefix(frame, "{") {
					frames = append(frames, frame)
				}
				remaining = rest[frameLen:]
				continue
			}
		}

		lineEnd := strings.IndexAny(remaining, "\r\n")
		if lineEnd < 0 {
			line := strings.TrimSpace(remaining)
			if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
				frames = append(frames, line)
			}
			break
		}

		line := strings.TrimSpace(remaining[:lineEnd])
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
			frames = append(frames, line)
		}
		remaining = remaining[lineEnd+1:]
	}

	return frames
}

func writeOpenAIStreamError(w io.Writer, id string, created int64, model string, err error) {
	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"error": map[string]string{
			"message": err.Error(),
			"type":    "upstream_parse_error",
		},
		"choices": []map[string]interface{}{},
	}
	bytes, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", bytes)
	w.(http.Flusher).Flush()
}

func sendSSERole(w io.Writer, id string, created int64, model string) {
	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]string{
					"role": "assistant",
				},
				"finish_reason": nil,
			},
		},
	}
	bytes, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", bytes)
	w.(http.Flusher).Flush()
}

func sendSSE(w io.Writer, id string, created int64, model, content string) {
	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]string{
					"content": content,
				},
				"finish_reason": nil,
			},
		},
	}
	bytes, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", bytes)
	w.(http.Flusher).Flush()
}

func sendSSEThinking(w io.Writer, id string, created int64, model, thinking string) {
	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]string{
					"reasoning_content": thinking,
					"content":           "",
				},
				"finish_reason": nil,
			},
		},
	}
	bytes, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", bytes)
	w.(http.Flusher).Flush()
}

func sendSSEToolCalls(w io.Writer, id string, created int64, model string, toolCalls []service.ToolCall) {
	for _, tc := range toolCalls {
		resp := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{
							{
								"index": 0,
								"id":    tc.ID,
								"type":  "function",
								"function": map[string]string{
									"name":      tc.Function.Name,
									"arguments": tc.Function.Arguments,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		}
		bytes, _ := json.Marshal(resp)
		fmt.Fprintf(w, "data: %s\n\n", bytes)
		w.(http.Flusher).Flush()
	}
}

var imagePlaceholderRegex = regexp.MustCompile(`\s*https?://googleusercontent\.com/image_generation_content/\d+\s*`)

func filterImagePlaceholders(text string) string {
	return imagePlaceholderRegex.ReplaceAllString(text, "")
}

func mimeTypeToFileExt(mimeType string) string {
	switch strings.Split(strings.ToLower(mimeType), ";")[0] {
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	default:
		return ""
	}
}

func parseGeminiResponseFromBytes(content []byte, onChunk func(text, thought string, imgURL string)) {
	var allParts []gjson.Result

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimPrefix(line, ")]}'")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		outer := gjson.Parse(line)
		if !outer.IsArray() {
			continue
		}

		outer.ForEach(func(_, part gjson.Result) bool {
			allParts = append(allParts, part)
			return true
		})
	}

	if len(allParts) == 0 {
		return
	}

	bodyIndex := -1
	var body gjson.Result

	for i, part := range allParts {
		dataStr := part.Get("2").String()
		if dataStr == "" {
			continue
		}
		inner := gjson.Parse(dataStr)
		if inner.Get("4").Exists() {
			bodyIndex = i
			body = inner
			break
		}
	}

	if bodyIndex < 0 || !body.Exists() {
		return
	}

	candidateArr := body.Get("4").Array()
	for candIdx, candidate := range candidateArr {
		text := candidate.Get("1.0").String()
		thoughts := candidate.Get("37.0.0").String()

		text = strings.ReplaceAll(text, `\<`, `<`)
		text = strings.ReplaceAll(text, `\>`, `>`)
		text = strings.ReplaceAll(text, `\_`, `_`)
		text = strings.ReplaceAll(text, `\[`, `[`)
		text = strings.ReplaceAll(text, `\]`, `]`)
		text = filterImagePlaceholders(text)

		var imgURL string

		if candidate.Get("12.7.0").Exists() {
			for i := bodyIndex; i < len(allParts); i++ {
				imgDataStr := allParts[i].Get("2").String()
				if imgDataStr == "" {
					continue
				}
				imgInner := gjson.Parse(imgDataStr)
				imgCandidate := imgInner.Get(fmt.Sprintf("4.%d", candIdx))
				if !imgCandidate.Get("12.7.0").Exists() {
					continue
				}

				if finishedText := imgCandidate.Get("1.0").String(); finishedText != "" {
					text = filterImagePlaceholders(finishedText)
					text = strings.ReplaceAll(text, `\<`, `<`)
					text = strings.ReplaceAll(text, `\>`, `>`)
					text = strings.ReplaceAll(text, `\_`, `_`)
					text = strings.ReplaceAll(text, `\[`, `[`)
					text = strings.ReplaceAll(text, `\]`, `]`)
				}

				imgCandidate.Get("12.7.0").ForEach(func(_, genImg gjson.Result) bool {
					url := genImg.Get("0.3.3").String()
					if url != "" && !strings.HasPrefix(url, "http://googleusercontent.com/image_generation_content") {
						imgURL = url
					}
					return true
				})

				if imgURL != "" {
					break
				}
			}
		}

		onChunk(text, thoughts, imgURL)
	}
}

func downloadImageAsBase64(url string, cookies map[string]string) string {
	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("[Images] Failed to create request: %v", err)
		return ""
	}

	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Images] Failed to download image: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[Images] Image download returned status %d", resp.StatusCode)
		return ""
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) == 0 {
		log.Printf("[Images] Failed to read image data: %v", err)
		return ""
	}

	log.Printf("[Images] Downloaded image: %d bytes", len(data))
	return base64.StdEncoding.EncodeToString(data)
}

func extractImageURLsFromResponse(reader io.Reader) []string {
	var urls []string
	var allParts []gjson.Result

	content, _ := io.ReadAll(reader)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimPrefix(line, ")]}'")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		outer := gjson.Parse(line)
		if !outer.IsArray() {
			continue
		}

		outer.ForEach(func(_, part gjson.Result) bool {
			allParts = append(allParts, part)
			return true
		})
	}

	if len(allParts) == 0 {
		return urls
	}

	bodyIndex := -1
	for i, part := range allParts {
		dataStr := part.Get("2").String()
		if dataStr == "" {
			continue
		}
		inner := gjson.Parse(dataStr)
		if inner.Get("4").Exists() {
			bodyIndex = i
			break
		}
	}

	if bodyIndex < 0 {
		return urls
	}

	for i := bodyIndex; i < len(allParts); i++ {
		imgDataStr := allParts[i].Get("2").String()
		if imgDataStr == "" {
			continue
		}
		imgInner := gjson.Parse(imgDataStr)
		imgCandidate := imgInner.Get("4.0")
		if !imgCandidate.Get("12.7.0").Exists() {
			continue
		}

		imgCandidate.Get("12.7.0").ForEach(func(_, genImg gjson.Result) bool {
			url := genImg.Get("0.3.3").String()
			if url != "" && !strings.HasPrefix(url, "http://googleusercontent.com/image_generation_content") {
				urls = append(urls, url)
			}
			return true
		})

		if len(urls) > 0 {
			break
		}
	}

	return urls
}

func handleImageChatRequest(c *gin.Context, client *gemini.Client, req ChatRequest) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	// Extract prompt from last user message
	var prompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if strings.EqualFold(req.Messages[i].Role, "user") {
			switch content := req.Messages[i].Content.(type) {
			case string:
				prompt = content
			case []interface{}:
				for _, part := range content {
					if m, ok := part.(map[string]interface{}); ok {
						if text, ok := m["text"].(string); ok {
							prompt = text
							break
						}
					}
				}
			}
			break
		}
	}

	if prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No prompt found in messages"})
		return
	}

	respBody, err := client.StreamGenerateContent(fmt.Sprintf("Generate an image of %s", prompt), req.Model, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer respBody.Close()

	imageURLs := extractImageURLsFromResponse(respBody)

	if len(imageURLs) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "No images generated",
			"type":    "server_error",
		}})
		return
	}

	// Download images using gemini client
	var content strings.Builder
	for i, imgURL := range imageURLs {
		fullURL := imgURL
		if !strings.Contains(fullURL, "=s") {
			fullURL = imgURL + "=s2048"
		}
		data, err := client.FetchImage(fullURL)
		if err != nil {
			log.Printf("[Images] Failed to fetch image: %v", err)
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		content.WriteString(fmt.Sprintf("![Generated Image %d](data:image/png;base64,%s)\n\n", i+1, b64))
	}

	if content.Len() == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "Failed to download images",
			"type":    "server_error",
		}})
		return
	}

	if req.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		sendSSERole(c.Writer, id, created, req.Model)
		sendSSE(c.Writer, id, created, req.Model, content.String())
		fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		c.Writer.(http.Flusher).Flush()
	} else {
		c.JSON(http.StatusOK, gin.H{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   req.Model,
			"choices": []gin.H{
				{
					"index": 0,
					"message": gin.H{
						"role":    "assistant",
						"content": content.String(),
					},
					"finish_reason": "stop",
				},
			},
		})
	}
}
