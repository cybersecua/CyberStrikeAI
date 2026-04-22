package debug

import (
	"testing"

	"go.uber.org/zap"
)

func TestNoopSink_Disabled(t *testing.T) {
	s := NewSink(false, nil, zap.NewNop())
	if s.Enabled() {
		t.Fatalf("NewSink(false) should return a disabled sink")
	}
	// All methods must be safe to call with nil db.
	s.StartSession("conv-a")
	s.EndSession("conv-a", "completed")
	s.RecordLLMCall("conv-a", "msg-1", LLMCall{})
	s.RecordEvent("conv-a", "msg-1", Event{})
	s.SetEnabled(true)
	if s.Enabled() {
		t.Fatalf("noopSink.SetEnabled(true) must stay disabled — only dbSink has runtime toggle")
	}
}
