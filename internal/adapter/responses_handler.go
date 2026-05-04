package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gemini-web2api/internal/balancer"
	"gemini-web2api/internal/config"
	"gemini-web2api/internal/gemini"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ResponsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Stream       bool            `json:"stream"`
	Instructions string          `json:"instructions,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	MaxTokens    *int            `json:"max_output_tokens,omitempty"`
	Tools        []interface{}   `json:"tools,omitempty"`
}

type ResponsesInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func ResponsesHandler(pool *balancer.AccountPool) gin.HandlerFunc {
	return func(c *gin.Context) {
		entry, ok := pool.Next()
		if !ok || entry == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"message": "No available accounts",
				"type":    "server_error",
			}})
			return
		}
		client := entry.Client
		c.Set("account_id", entry.AccountID)

		var req ResponsesRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"message": fmt.Sprintf("Invalid request body: %v", err),
				"type":    "invalid_request_error",
			}})
			return
		}

		mappedModel := config.MapModel(req.Model)
		prompt, files := buildResponsesPrompt(&req, client)

		gemini.RandomDelay()

		respBody, err := client.StreamGenerateContent(prompt, mappedModel, files, nil)
		if err != nil {
			log.Printf("[Responses] Gemini request failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"message": fmt.Sprintf("Failed to communicate with Gemini: %v", err),
				"type":    "server_error",
			}})
			return
		}
		defer respBody.Close()

		responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
		created := time.Now().Unix()

		if req.Stream {
			handleResponsesStream(c, respBody, responseID, created, req.Model)
		} else {
			handleResponsesNonStream(c, respBody, responseID, created, req.Model)
		}
	}
}

func buildResponsesPrompt(req *ResponsesRequest, client *gemini.Client) (string, []gemini.FileData) {
	var builder strings.Builder
	var files []gemini.FileData

	if req.Instructions != "" {
		builder.WriteString(fmt.Sprintf("**System**: %s\n\n", req.Instructions))
	}

	var inputStr string
	if err := json.Unmarshal(req.Input, &inputStr); err == nil {
		builder.WriteString(fmt.Sprintf("**User**: %s\n\n", inputStr))
		return builder.String(), files
	}

	var messages []ResponsesInputMessage
	if err := json.Unmarshal(req.Input, &messages); err == nil {
		for _, msg := range messages {
			role := "User"
			switch msg.Role {
			case "assistant", "model":
				role = "Model"
			case "system", "developer":
				role = "System"
			}

			var contentStr string
			if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
				builder.WriteString(fmt.Sprintf("**%s**: %s\n\n", role, contentStr))
				continue
			}

			var contentParts []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &contentParts); err == nil {
				builder.WriteString(fmt.Sprintf("**%s**: ", role))
				for _, part := range contentParts {
					if text, ok := part["text"].(string); ok {
						builder.WriteString(text)
					} else if part["type"] == "input_image" {
						if imgData, ok := part["image_url"].(string); ok {
							if strings.HasPrefix(imgData, "data:") {
								parts := strings.SplitN(imgData, ",", 2)
								if len(parts) == 2 {
									data, err := base64.StdEncoding.DecodeString(parts[1])
									if err == nil {
										fname := fmt.Sprintf("image_%d.png", time.Now().UnixNano())
										fid, err := client.UploadFile(data, fname)
										if err == nil {
											files = append(files, gemini.FileData{URL: fid, FileName: fname})
											builder.WriteString("[Image]")
										}
									}
								}
							} else if strings.HasPrefix(imgData, "http") {
								fd, err := client.DownloadAndUpload(imgData)
								if err == nil {
									files = append(files, fd)
									builder.WriteString("[Image]")
								} else {
									log.Printf("[Responses] Failed to download image: %v", err)
								}
							}
						}
					} else if part["type"] == "input_file" {
						if fileData, ok := part["file_data"].(string); ok {
							fname, _ := part["filename"].(string)
							if fname == "" {
								fname = fmt.Sprintf("file_%d", time.Now().UnixNano())
							}
							data, err := base64.StdEncoding.DecodeString(fileData)
							if err == nil {
								fid, err := client.UploadFile(data, fname)
								if err == nil {
									files = append(files, gemini.FileData{URL: fid, FileName: fname})
									builder.WriteString(fmt.Sprintf("[File: %s]", fname))
								}
							}
						} else if fileURL, ok := part["file_url"].(string); ok {
							fd, err := client.DownloadAndUpload(fileURL)
							if err == nil {
								files = append(files, fd)
								builder.WriteString(fmt.Sprintf("[File: %s]", fd.FileName))
							}
						}
					}
				}
				builder.WriteString("\n\n")
			}
		}
	}

	prompt := builder.String()
	if prompt == "" {
		prompt = "Hello"
	}

	return prompt, files
}

func handleResponsesNonStream(c *gin.Context, respBody io.ReadCloser, responseID string, created int64, model string) {
	var fullText strings.Builder
	var fullThinking strings.Builder

	parseStatus, parseErr := parseGeminiResponse(respBody, func(text, thought string) {
		fullText.WriteString(text)
		fullThinking.WriteString(thought)
	})
	if err := geminiParseError(parseStatus, parseErr); err != nil {
		log.Printf("[Responses] Failed to parse Gemini response: %v", err)
		respondWithGeminiError(c, err)
		return
	}

	output := []gin.H{}

	if fullThinking.Len() > 0 {
		output = append(output, gin.H{
			"type": "reasoning",
			"id":   fmt.Sprintf("rs_%d", time.Now().UnixNano()),
			"summary": []gin.H{
				{
					"type": "summary_text",
					"text": fullThinking.String(),
				},
			},
		})
	}

	output = append(output, gin.H{
		"type": "message",
		"id":   fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		"role": "assistant",
		"content": []gin.H{
			{
				"type": "output_text",
				"text": fullText.String(),
			},
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"id":         responseID,
		"object":     "response",
		"created_at": created,
		"model":      model,
		"output":     output,
		"status":     "completed",
		"usage":      gin.H{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	})
}

func handleResponsesStream(c *gin.Context, respBody io.ReadCloser, responseID string, created int64, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	w := c.Writer
	seq := 0

	writeEvent := func(eventType string, data interface{}) {
		bytes, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, bytes)
		w.(http.Flusher).Flush()
		seq++
	}

	writeEvent("response.created", gin.H{
		"response": gin.H{
			"id":         responseID,
			"object":     "response",
			"created_at": created,
			"model":      model,
			"status":     "in_progress",
			"output":     []interface{}{},
		},
		"sequence_number": seq,
	})

	outputItemID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	contentIdx := 0

	writeEvent("response.output_item.added", gin.H{
		"output_index": 0,
		"item": gin.H{
			"type": "message",
			"id":   outputItemID,
			"role": "assistant",
		},
		"sequence_number": seq,
	})

	writeEvent("response.content_part.added", gin.H{
		"output_index":  0,
		"content_index": contentIdx,
		"part": gin.H{
			"type": "output_text",
			"text": "",
		},
		"sequence_number": seq,
	})

	thinkingStarted := false

	c.Stream(func(cw io.Writer) bool {
		parseStatus, parseErr := parseGeminiResponse(respBody, func(text, thought string) {
			if thought != "" {
				if !thinkingStarted {
					thinkingStarted = true
					writeEvent("response.output_item.added", gin.H{
						"output_index": 0,
						"item": gin.H{
							"type": "reasoning",
							"id":   fmt.Sprintf("rs_%d", time.Now().UnixNano()),
						},
						"sequence_number": seq,
					})
				}
				writeEvent("response.reasoning_summary_text.delta", gin.H{
					"output_index":    0,
					"summary_index":   0,
					"delta":           thought,
					"sequence_number": seq,
				})
			}
			if text != "" {
				writeEvent("response.output_text.delta", gin.H{
					"output_index":    0,
					"content_index":   contentIdx,
					"delta":           text,
					"sequence_number": seq,
				})
			}
		})
		if err := geminiParseError(parseStatus, parseErr); err != nil {
			log.Printf("[Responses] Failed to parse Gemini stream: %v", err)
			writeEvent("response.failed", gin.H{
				"response": gin.H{
					"id":         responseID,
					"object":     "response",
					"created_at": created,
					"model":      model,
					"status":     "failed",
					"error": gin.H{
						"message": err.Error(),
						"type":    "upstream_parse_error",
					},
				},
				"sequence_number": seq,
			})
		}
		return false
	})

	writeEvent("response.output_text.done", gin.H{
		"output_index":    0,
		"content_index":   contentIdx,
		"sequence_number": seq,
	})

	writeEvent("response.output_item.done", gin.H{
		"output_index": 0,
		"item": gin.H{
			"type": "message",
			"id":   outputItemID,
			"role": "assistant",
		},
		"sequence_number": seq,
	})

	writeEvent("response.completed", gin.H{
		"response": gin.H{
			"id":         responseID,
			"object":     "response",
			"created_at": created,
			"model":      model,
			"status":     "completed",
		},
		"sequence_number": seq,
	})
}
