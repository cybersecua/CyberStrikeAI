package handler

import (
	"testing"
)

func TestMajorEventStep_ReturnsEmptyForFilteredEvents(t *testing.T) {
	cases := []string{"thinking_stream_start", "thinking_stream_delta", "tool_result_delta", "response_delta", "done"}
	for _, ev := range cases {
		got := MajorEventStep(ev, "", nil)
		if got != "" {
			t.Errorf("event %q should be filtered (return ''), got %q", ev, got)
		}
	}
}

func TestMajorEventStep_IterationFormatsRoundNumber(t *testing.T) {
	got := MajorEventStep("iteration", "", map[string]interface{}{"iteration": 2})
	want := "🤔 Round 2: thinking…"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestMajorEventStep_ToolCallShowsToolName(t *testing.T) {
	got := MajorEventStep("tool_call", "", map[string]interface{}{
		"toolName":  "nmap",
		"iteration": 3,
	})
	want := "🔧 Round 3: calling nmap…"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestMajorEventStep_ToolResultBranchesOnSuccess(t *testing.T) {
	ok := MajorEventStep("tool_result", "", map[string]interface{}{
		"toolName":  "nmap",
		"iteration": 3,
		"success":   true,
	})
	if ok != "✅ Round 3: nmap done" {
		t.Fatalf("success: got %q", ok)
	}
	fail := MajorEventStep("tool_result", "", map[string]interface{}{
		"toolName":  "nmap",
		"iteration": 3,
		"success":   false,
	})
	if fail != "❌ Round 3: nmap failed" {
		t.Fatalf("failure: got %q", fail)
	}
}

func TestMajorEventStep_ResponseStart(t *testing.T) {
	got := MajorEventStep("response_start", "", nil)
	if got != "✍️ Drafting answer…" {
		t.Fatalf("got %q", got)
	}
}
