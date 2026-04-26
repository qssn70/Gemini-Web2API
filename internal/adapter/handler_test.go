package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGeminiResponse_ParsesContentPartsFormat(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": map[string]interface{}{
					"1": []interface{}{
						[]interface{}{"Hello "},
						[]interface{}{"thought", nil, true},
						[]interface{}{"world"},
					},
				},
			},
		},
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}

	outer := []interface{}{
		[]interface{}{nil, nil, string(innerJSON)},
	}
	outerJSON, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("marshal outer: %v", err)
	}

	reader := strings.NewReader(")]}'" + string(outerJSON) + "\n")

	var gotText strings.Builder
	var gotThought strings.Builder
	parseGeminiResponse(reader, func(text, thought string) {
		gotText.WriteString(text)
		gotThought.WriteString(thought)
	})

	if got := gotText.String(); got != "Hello world" {
		t.Fatalf("expected text %q, got %q", "Hello world", got)
	}
	if got := gotThought.String(); got != "thought" {
		t.Fatalf("expected thought %q, got %q", "thought", got)
	}
}
