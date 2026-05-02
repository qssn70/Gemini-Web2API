package adapter

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

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
	status, err := parseGeminiResponse(reader, func(text, thought string) {
		gotText.WriteString(text)
		gotThought.WriteString(thought)
	})

	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized {
		t.Fatalf("expected response to be recognized")
	}
	if !status.Emitted {
		t.Fatalf("expected response to emit content")
	}

	if got := gotText.String(); got != "Hello world" {
		t.Fatalf("expected text %q, got %q", "Hello world", got)
	}
	if got := gotThought.String(); got != "thought" {
		t.Fatalf("expected thought %q, got %q", "thought", got)
	}
}

func TestParseGeminiResponse_RecognizesEmptyContentPartsFormat(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": map[string]interface{}{
					"1": []interface{}{},
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

	status, err := parseGeminiResponse(strings.NewReader(")]}'"+string(outerJSON)+"\n"), func(text, thought string) {
		t.Fatalf("expected no emitted chunks, got text=%q thought=%q", text, thought)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized {
		t.Fatalf("expected empty content parts format to be recognized")
	}
	if status.Emitted {
		t.Fatalf("expected empty content parts format not to emit chunks")
	}
	if err := geminiParseError(status, err); err != nil {
		t.Fatalf("expected recognized empty output not to be parse error, got %v", err)
	}
}

func TestParseGeminiResponse_UnknownStructureIsUnrecognized(t *testing.T) {
	status, err := parseGeminiResponse(strings.NewReader(`[[null,null,"{}"]]`+"\n"), func(text, thought string) {
		t.Fatalf("expected no chunks, got text=%q thought=%q", text, thought)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if status.Recognized {
		t.Fatalf("expected unknown structure not to be recognized")
	}
	if err := geminiParseError(status, err); err == nil {
		t.Fatalf("expected unknown structure to produce parse error")
	}
}

func TestParseGeminiResponse_ParsesLengthPrefixedFrames(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": []interface{}{"Hello from frame"},
			},
		},
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	frame := `[[null,null,` + string(mustMarshalJSONString(t, string(innerJSON))) + `]]`
	body := ")]}'\n" + strconv.Itoa(len(frame)) + "\n" + frame + "\n"

	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted {
		t.Fatalf("expected recognized emitted frame, got %+v", status)
	}
	if got.String() != "Hello from frame" {
		t.Fatalf("expected frame text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesWrbFrRowPayload(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": []interface{}{"Hello from wrb"},
			},
		},
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	frameValue := []interface{}{"wrb.fr", "rpc-id", string(innerJSON)}
	frameBytes, err := json.Marshal(frameValue)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	body := ")]}'\n" + string(frameBytes) + "\n"

	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted {
		t.Fatalf("expected recognized emitted wrb row, got %+v", status)
	}
	if got.String() != "Hello from wrb" {
		t.Fatalf("expected wrb text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesNestedWrbFrRows(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": []interface{}{"Hello from nested wrb"},
			},
		},
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	frameValue := []interface{}{
		[]interface{}{"noop", "ignored"},
		[]interface{}{"wrb.fr", "rpc-id", string(innerJSON)},
	}
	frameBytes, err := json.Marshal(frameValue)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	body := ")]}'\n" + strconv.Itoa(len(frameBytes)) + "\n" + string(frameBytes) + "\n"

	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted {
		t.Fatalf("expected recognized emitted nested wrb row, got %+v", status)
	}
	if got.String() != "Hello from nested wrb" {
		t.Fatalf("expected nested wrb text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesCardTextFallback(t *testing.T) {
	inner := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"22": []interface{}{"Card fallback text"},
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

	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(")]}'"+string(outerJSON)+"\n"), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted {
		t.Fatalf("expected recognized emitted fallback, got %+v", status)
	}
	if got.String() != "Card fallback text" {
		t.Fatalf("expected fallback text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesWrappedPayloadCandidateSet(t *testing.T) {
	partJSON := []interface{}{
		map[string]interface{}{
			"4": []interface{}{
				map[string]interface{}{
					"1": []interface{}{"Hello from wrapped payload"},
				},
			},
		},
	}

	body := buildWrbBody(t, partJSON)
	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted || status.CandidateSet == 0 {
		t.Fatalf("expected wrapped payload candidate set, got %+v", status)
	}
	if got.String() != "Hello from wrapped payload" {
		t.Fatalf("expected wrapped payload text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesObjectStyleCandidates(t *testing.T) {
	partJSON := map[string]interface{}{
		"response": map[string]interface{}{
			"candidates": []interface{}{
				map[string]interface{}{
					"1": []interface{}{"Hello from object candidates"},
				},
			},
		},
	}

	body := buildWrbBody(t, partJSON)
	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted || status.CandidateSet == 0 {
		t.Fatalf("expected object-style candidate set, got %+v", status)
	}
	if got.String() != "Hello from object candidates" {
		t.Fatalf("expected object candidates text, got %q", got.String())
	}
}

func TestParseGeminiResponse_ParsesNestedPayloadJSONString(t *testing.T) {
	nested := map[string]interface{}{
		"4": []interface{}{
			map[string]interface{}{
				"1": []interface{}{"Hello from nested JSON string"},
			},
		},
	}
	nestedBytes, err := json.Marshal(nested)
	if err != nil {
		t.Fatalf("marshal nested: %v", err)
	}
	partJSON := []interface{}{string(nestedBytes)}

	body := buildWrbBody(t, partJSON)
	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted || status.CandidateSet == 0 {
		t.Fatalf("expected nested JSON string candidate set, got %+v", status)
	}
	if got.String() != "Hello from nested JSON string" {
		t.Fatalf("expected nested JSON text, got %q", got.String())
	}
}

func TestParseGeminiResponse_FindsDeepCandidateSet(t *testing.T) {
	partJSON := map[string]interface{}{
		"control": []interface{}{"metadata", 123},
		"nested": map[string]interface{}{
			"unexpected": []interface{}{
				map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"1": []interface{}{"Hello from deep candidate"},
						},
					},
				},
			},
		},
	}

	body := buildWrbBody(t, partJSON)
	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted || status.CandidateSet == 0 || status.MatchedNodes == 0 {
		t.Fatalf("expected deep candidate discovery, got %+v", status)
	}
	if got.String() != "Hello from deep candidate" {
		t.Fatalf("expected deep candidate text, got %q", got.String())
	}
}

func TestParseGeminiResponse_SkipsNumericMetadataCandidate(t *testing.T) {
	partJSON := map[string]interface{}{
		"nested": []interface{}{
			[]interface{}{
				"metadata",
				[]interface{}{1026},
			},
		},
	}

	body := buildWrbBody(t, partJSON)
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		t.Fatalf("expected numeric metadata not to emit chunks, got text=%q thought=%q", text, thought)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if status.Recognized || status.Emitted || status.CandidateSet != 0 {
		t.Fatalf("expected numeric metadata not to be recognized as candidate output, got %+v", status)
	}
}

func TestParseGeminiResponse_AllowsNumericStringOutput(t *testing.T) {
	partJSON := map[string]interface{}{
		"nested": []interface{}{
			[]interface{}{
				map[string]interface{}{
					"1": []interface{}{"1026"},
				},
			},
		},
	}

	body := buildWrbBody(t, partJSON)
	var got strings.Builder
	status, err := parseGeminiResponse(strings.NewReader(body), func(text, thought string) {
		got.WriteString(text)
	})
	if err != nil {
		t.Fatalf("parseGeminiResponse returned error: %v", err)
	}
	if !status.Recognized || !status.Emitted || status.CandidateSet == 0 {
		t.Fatalf("expected numeric string output to remain valid, got %+v", status)
	}
	if got.String() != "1026" {
		t.Fatalf("expected numeric string output, got %q", got.String())
	}
}

func buildWrbBody(t *testing.T, payload interface{}) string {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	frameValue := []interface{}{"wrb.fr", "rpc-id", string(payloadBytes)}
	frameBytes, err := json.Marshal(frameValue)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return ")]}'\n" + string(frameBytes) + "\n"
}

func mustMarshalJSONString(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return b
}

func TestParseGeminiResponse_PropagatesScannerError(t *testing.T) {
	status, err := parseGeminiResponse(errorReader{}, func(text, thought string) {
		t.Fatalf("expected no chunks, got text=%q thought=%q", text, thought)
	})
	if err == nil {
		t.Fatalf("expected scanner error")
	}
	if status.Recognized {
		t.Fatalf("expected failed reader not to be recognized")
	}
	if wrapped := geminiParseError(status, err); !errors.Is(wrapped, err) {
		t.Fatalf("expected wrapped error to contain scanner error, got %v", wrapped)
	}
}

func TestGeminiParseError_AllowsRecognizedEmptyOutput(t *testing.T) {
	if err := geminiParseError(geminiParseStatus{Recognized: true}, nil); err != nil {
		t.Fatalf("expected recognized empty output to be allowed, got %v", err)
	}
}

var _ io.Reader = errorReader{}
