package service

import "testing"

func TestParseToolCalls_ParsesSingleToolCallAndRemovesMarkup(t *testing.T) {
	input := "before\n[ToolCalls]\n[Call:getWeather]\n[CallParameter:city]\n\"Tokyo\"\n[/CallParameter:city]\n[/Call:getWeather]\n[/ToolCalls]\nafter"

	calls, clean := ParseToolCalls(input)

	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "getWeather" {
		t.Fatalf("expected function name getWeather, got %q", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"city":"Tokyo"}` {
		t.Fatalf("expected arguments %q, got %q", `{"city":"Tokyo"}`, calls[0].Function.Arguments)
	}
	if clean != "before\nafter" {
		t.Fatalf("expected cleaned text %q, got %q", "before\nafter", clean)
	}
}
