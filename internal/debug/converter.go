package debug

import (
	"encoding/json"
	"errors"
)

// LLMCallRow is the row-shape read from debug_llm_calls by the
// exporter layer. Kept separate from LLMCall (the write-side value
// type carried by the sink) so the read path can evolve without
// breaking Sink callers.
type LLMCallRow struct {
	ID               int64
	ConversationID   string
	MessageID        string
	Iteration        int
	CallIndex        int
	AgentID          string
	SentAt           int64
	FirstTokenAt     int64
	FinishedAt       int64
	PromptTokens     int64
	CompletionTokens int64
	RequestJSON      string
	ResponseJSON     string
	Error            string
}

// shareGPTRequest is the request shape we expect: the orchestrator
// always sends {messages: [...], tools: [...]} per the openai-format
// contract. Only messages is needed for the export.
type shareGPTRequest struct {
	Messages []json.RawMessage `json:"messages"`
}

// shareGPTResponse is the response shape.
type shareGPTResponse struct {
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Message      json.RawMessage `json:"message"`
	} `json:"choices"`
}

// ToShareGPT produces a single JSONL line (no trailing newline) of
// {"messages": [...]} matching the OpenAI / ShareGPT / HuggingFace
// SFT loader contract.
//
// Algorithm per spec §Export:
//   1. Filter to orchestrator-level calls only (agent_id =
//      "cyberstrike-orchestrator"). Sub-agent traces are excluded
//      from the training output — they're internal delegation detail
//      available via format=raw only.
//   2. Pick the terminal call: the first call whose
//      response.choices[0].finish_reason == "stop" (final text
//      answer, not another tool-calls round). If no such call exists
//      (orchestrator hit maxIter without a clean stop), fall back to
//      the last call in input order.
//   3. Output = request.messages + [response.choices[0].message]
//      as one JSONL line. The terminal request.messages already has
//      all prior assistant tool_calls and tool-role responses
//      interleaved by the orchestrator's history-append loop.
func ToShareGPT(calls []LLMCallRow) ([]byte, error) {
	if len(calls) == 0 {
		return nil, errors.New("ToShareGPT: empty input")
	}
	var orch []LLMCallRow
	for _, c := range calls {
		if c.AgentID == "cyberstrike-orchestrator" {
			orch = append(orch, c)
		}
	}
	if len(orch) == 0 {
		return nil, errors.New("ToShareGPT: no orchestrator-level calls in input")
	}

	chosen := -1
	for i, c := range orch {
		var resp shareGPTResponse
		if err := json.Unmarshal([]byte(c.ResponseJSON), &resp); err != nil {
			continue
		}
		if len(resp.Choices) > 0 && resp.Choices[0].FinishReason == "stop" {
			chosen = i
			break
		}
	}
	if chosen == -1 {
		chosen = len(orch) - 1
	}
	c := orch[chosen]

	var req shareGPTRequest
	if err := json.Unmarshal([]byte(c.RequestJSON), &req); err != nil {
		return nil, err
	}
	var resp shareGPTResponse
	if err := json.Unmarshal([]byte(c.ResponseJSON), &resp); err != nil {
		return nil, err
	}

	out := struct {
		Messages []json.RawMessage `json:"messages"`
	}{Messages: req.Messages}
	if len(resp.Choices) > 0 && len(resp.Choices[0].Message) > 0 {
		out.Messages = append(out.Messages, resp.Choices[0].Message)
	}
	return json.Marshal(out)
}
