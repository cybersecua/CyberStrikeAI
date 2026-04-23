package debug

import (
	"encoding/json"
	"strings"
	"testing"
)

func orchestratorCall(req, resp string) LLMCallRow {
	return LLMCallRow{AgentID: "cyberstrike-orchestrator", SentAt: 1, RequestJSON: req, ResponseJSON: resp}
}

func TestToShareGPT_PicksFirstStopCall(t *testing.T) {
	calls := []LLMCallRow{
		orchestratorCall(
			`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`,
			`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"nmap","arguments":"{}"}}]}}]}`,
		),
		orchestratorCall(
			`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"nmap","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"port 22 open"}]}`,
			`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"SSH is open on 22."}}]}`,
		),
	}
	out, err := ToShareGPT(calls)
	if err != nil {
		t.Fatalf("ToShareGPT: %v", err)
	}
	var got struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, out)
	}
	if len(got.Messages) != 5 {
		t.Fatalf("want 5 messages (sys,user,assistant-with-tc,tool,assistant-final), got %d: %s", len(got.Messages), out)
	}
	lastRole, _ := got.Messages[4]["role"].(string)
	lastContent, _ := got.Messages[4]["content"].(string)
	if lastRole != "assistant" || !strings.Contains(lastContent, "SSH is open") {
		t.Fatalf("final assistant missing or wrong: %v", got.Messages[4])
	}
}

func TestToShareGPT_ExcludesSubAgentCalls(t *testing.T) {
	calls := []LLMCallRow{
		orchestratorCall(
			`{"messages":[{"role":"user","content":"scan"}]}`,
			`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`,
		),
		{AgentID: "recon-subagent", SentAt: 2, RequestJSON: `{"messages":[{"role":"user","content":"inner"}]}`, ResponseJSON: `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"inner done"}}]}`},
	}
	out, err := ToShareGPT(calls)
	if err != nil {
		t.Fatalf("ToShareGPT: %v", err)
	}
	if strings.Contains(string(out), "inner") {
		t.Fatalf("sub-agent content leaked into ShareGPT export: %s", out)
	}
}

func TestToShareGPT_FallsBackToLastWhenNoStop(t *testing.T) {
	calls := []LLMCallRow{
		orchestratorCall(
			`{"messages":[{"role":"user","content":"a"}]}`,
			`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":""}}]}`,
		),
		orchestratorCall(
			`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":""},{"role":"tool","tool_call_id":"t","content":"r"}]}`,
			`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`,
		),
	}
	out, err := ToShareGPT(calls)
	if err != nil {
		t.Fatalf("ToShareGPT: %v", err)
	}
	if !strings.Contains(string(out), "partial") {
		t.Fatalf("fallback to last call failed: %s", out)
	}
}

func TestToShareGPT_EmptyInputReturnsError(t *testing.T) {
	if _, err := ToShareGPT(nil); err == nil {
		t.Fatalf("want error on empty input, got nil")
	}
}

func TestToShareGPT_NoOrchestratorCallsReturnsError(t *testing.T) {
	// Only sub-agent calls — nothing for the training-data export to emit.
	calls := []LLMCallRow{
		{AgentID: "recon-subagent", SentAt: 1, RequestJSON: `{"messages":[]}`, ResponseJSON: `{"choices":[]}`},
	}
	if _, err := ToShareGPT(calls); err == nil {
		t.Fatalf("want error when no orchestrator-level calls, got nil")
	}
}
