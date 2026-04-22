package debug

// LLMCall is one LLM round-trip recorded to debug_llm_calls.
// Zero-valued fields serialize to NULL columns (first_token_at,
// prompt_tokens, completion_tokens) for backends that don't report
// them — see the plan's "Deviations from spec" note on streaming
// token usage.
type LLMCall struct {
	Iteration        int
	CallIndex        int
	AgentID          string
	SentAt           int64 // unix nanos
	FirstTokenAt     int64 // 0 means unknown
	FinishedAt       int64
	PromptTokens     int64 // 0 means unknown
	CompletionTokens int64 // 0 means unknown
	RequestJSON      string
	ResponseJSON     string
	Error            string
}

// Event is one orchestrator/agent progress event recorded to
// debug_events. Seq is assigned by the sink, not the caller.
type Event struct {
	EventType   string
	AgentID     string
	PayloadJSON string
	StartedAt   int64
	FinishedAt  int64 // 0 means instant / no duration
}
