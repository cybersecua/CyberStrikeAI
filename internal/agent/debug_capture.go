package agent

import (
	"context"
	"encoding/json"
	"time"

	"cyberstrike-ai/internal/debug"
)

// captureLLMCall wraps one LLM round-trip and records it to the
// debug sink when capture coordinates are attached to ctx (via
// debug.WithCapture). requestPayload is marshaled to JSON BEFORE
// callFn runs, so even if the closure mutates it during dispatch
// the snapshot reflects the request at send time.
//
// When no capture coordinates are on ctx (debug off for this call),
// the only per-call overhead is: the CaptureCoords lookup, two
// time.Now() calls, and callFn itself. No JSON marshal cost.
func (a *Agent) captureLLMCall(
	ctx context.Context,
	requestPayload interface{},
	callFn func() (response interface{}, promptTokens, completionTokens int64, err error),
) (interface{}, error) {
	// Snapshot capture coordinates + request JSON BEFORE callFn. If
	// the caller or closure mutates requestPayload (e.g., toggling a
	// model field on fallback, or appending to a Messages slice),
	// the snapshot reflects the actual request at dispatch time.
	convID, msgID, iteration, callIndex, agentID := debug.CaptureCoords(ctx)
	var reqJSON []byte
	if convID != "" {
		reqJSON, _ = json.Marshal(requestPayload)
	}

	sentAt := time.Now().UnixNano()
	resp, pt, ct, err := callFn()
	finishedAt := time.Now().UnixNano()

	if convID == "" {
		// No capture coordinates — off-path. Skip recording. The
		// only overhead paid was two time.Now() calls and the
		// CaptureCoords lookup.
		return resp, err
	}

	var respJSON []byte
	if resp != nil {
		respJSON, _ = json.Marshal(resp)
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	a.debugSink.RecordLLMCall(convID, msgID, debug.LLMCall{
		Iteration:        iteration,
		CallIndex:        callIndex,
		AgentID:          agentID,
		SentAt:           sentAt,
		FinishedAt:       finishedAt,
		PromptTokens:     pt,
		CompletionTokens: ct,
		RequestJSON:      string(reqJSON),
		ResponseJSON:     string(respJSON),
		Error:            errStr,
	})
	return resp, err
}
