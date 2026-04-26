package claude

import (
	"context"
	"fmt"
	"testing"

	"go.uber.org/zap"
)

// mockClient implements the Client interface for testing.
type mockClient struct {
	result *Result
	err    error
}

func (m *mockClient) SendPrompt(_ context.Context, _ string, _ *PromptOptions) (*Result, error) {
	return m.result, m.err
}

func TestStreamAdapter_RunPrompt(t *testing.T) {
	logger := zap.NewNop()

	t.Run("successful prompt", func(t *testing.T) {
		client := &mockClient{
			result: &Result{
				Result:    "Hello from Claude",
				SessionID: "sess-123",
				NumTurns:  2,
				CostUSD:   0.01,
				Usage:     Usage{InputTokens: 100, OutputTokens: 50},
			},
		}
		adapter := NewStreamAdapterWithClient(client, logger)

		var events []string
		callback := func(eventType, message string, data interface{}) {
			events = append(events, eventType+":"+message)
		}

		result, sessionID, err := adapter.RunPrompt(context.Background(), "hello", "", "conv-1", nil, callback)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Hello from Claude" {
			t.Errorf("unexpected result: %s", result)
		}
		if sessionID != "sess-123" {
			t.Errorf("unexpected session ID: %s", sessionID)
		}
		if len(events) != 2 {
			t.Errorf("expected 2 events, got %d: %v", len(events), events)
		}
	})

	t.Run("session resumption", func(t *testing.T) {
		var capturedOpts *PromptOptions
		client := &mockClient{
			result: &Result{
				Result:    "resumed",
				SessionID: "sess-456",
			},
		}
		// Wrap to capture opts
		wrapper := &optsCaptureClient{inner: client, opts: &capturedOpts}
		adapter := NewStreamAdapterWithClient(wrapper, logger)

		// Pre-store a session
		adapter.sessions.Set("conv-2", "sess-existing")

		callback := func(_, _ string, _ interface{}) {}
		_, _, err := adapter.RunPrompt(context.Background(), "follow up", "", "conv-2", nil, callback)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if capturedOpts == nil || (*capturedOpts).SessionID != "sess-existing" {
			t.Errorf("expected session ID 'sess-existing' in opts, got: %+v", capturedOpts)
		}
	})

	t.Run("error from CLI", func(t *testing.T) {
		client := &mockClient{
			err: fmt.Errorf("binary not found"),
		}
		adapter := NewStreamAdapterWithClient(client, logger)

		callback := func(_, _ string, _ interface{}) {}
		_, _, err := adapter.RunPrompt(context.Background(), "hello", "", "conv-3", nil, callback)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestStreamAdapter_RunPrompt_RoleToolsFilter(t *testing.T) {
	logger := zap.NewNop()

	t.Run("nil roleTools includes all MCP tools", func(t *testing.T) {
		var capturedOpts *PromptOptions
		client := &mockClient{result: &Result{Result: "ok", SessionID: "s1"}}
		wrapper := &optsCaptureClient{inner: client, opts: &capturedOpts}
		adapter := NewStreamAdapterWithClient(wrapper, logger)
		adapter.cfg = CLIConfig{
			MCPToolNames: []string{"scan_host", "record_vulnerability", "webshell_exec"},
		}
		cb := func(_, _ string, _ interface{}) {}
		_, _, err := adapter.RunPrompt(context.Background(), "hi", "", "conv-rt1", nil, cb)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{
			"mcp__cyberstrike__scan_host":            true,
			"mcp__cyberstrike__record_vulnerability": true,
			"mcp__cyberstrike__webshell_exec":        true,
		}
		for _, tool := range (*capturedOpts).AllowedTools {
			delete(want, tool)
		}
		if len(want) > 0 {
			t.Errorf("missing tools in allowedTools: %v", want)
		}
	})

	t.Run("non-empty roleTools intersects MCP tool names", func(t *testing.T) {
		var capturedOpts *PromptOptions
		client := &mockClient{result: &Result{Result: "ok", SessionID: "s2"}}
		wrapper := &optsCaptureClient{inner: client, opts: &capturedOpts}
		adapter := NewStreamAdapterWithClient(wrapper, logger)
		adapter.cfg = CLIConfig{
			MCPToolNames: []string{"scan_host", "record_vulnerability", "webshell_exec"},
		}
		cb := func(_, _ string, _ interface{}) {}
		// roleTools restricts to only scan_host
		_, _, err := adapter.RunPrompt(context.Background(), "hi", "", "conv-rt2", []string{"scan_host"}, cb)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		allowed := make(map[string]bool)
		for _, t := range (*capturedOpts).AllowedTools {
			allowed[t] = true
		}
		if !allowed["mcp__cyberstrike__scan_host"] {
			t.Errorf("expected mcp__cyberstrike__scan_host to be allowed; got %v", (*capturedOpts).AllowedTools)
		}
		if allowed["mcp__cyberstrike__record_vulnerability"] {
			t.Errorf("expected mcp__cyberstrike__record_vulnerability to be excluded by roleTools")
		}
		if allowed["mcp__cyberstrike__webshell_exec"] {
			t.Errorf("expected mcp__cyberstrike__webshell_exec to be excluded by roleTools")
		}
	})
}

// optsCaptureClient wraps a Client to capture the PromptOptions.
type optsCaptureClient struct {
	inner Client
	opts  **PromptOptions
}

func (c *optsCaptureClient) SendPrompt(ctx context.Context, prompt string, opts *PromptOptions) (*Result, error) {
	*c.opts = opts
	return c.inner.SendPrompt(ctx, prompt, opts)
}
