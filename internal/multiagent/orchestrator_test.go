package multiagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberstrike-ai/internal/debug"
	_ "github.com/mattn/go-sqlite3"
)

// TestIsToolAllowed covers the execution-time role-whitelist gate. The
// assertion that actually matters here is the bypass protection: if
// roleTools names a restricted set, a tool outside that set must be
// rejected even if the caller somehow dispatched it. An empty slice
// means "no role restriction" and must allow anything.
func TestIsToolAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		roleTools []string
		tool      string
		want      bool
	}{
		{
			name:      "empty role list allows anything",
			roleTools: nil,
			tool:      "nmap",
			want:      true,
		},
		{
			name:      "empty slice allows anything",
			roleTools: []string{},
			tool:      "nuclei",
			want:      true,
		},
		{
			name:      "tool on whitelist is allowed",
			roleTools: []string{"nmap", "nuclei", "subfinder"},
			tool:      "nuclei",
			want:      true,
		},
		{
			name:      "tool not on whitelist is denied",
			roleTools: []string{"nmap", "nuclei"},
			tool:      "subfinder",
			want:      false,
		},
		{
			name:      "case-sensitive match: different case is denied",
			roleTools: []string{"nmap"},
			tool:      "Nmap",
			want:      false,
		},
		{
			name:      "exact-match: whitespace variants are denied",
			roleTools: []string{"nmap"},
			tool:      " nmap",
			want:      false,
		},
		{
			name:      "only one tool on the whitelist, matching",
			roleTools: []string{"record_vulnerability"},
			tool:      "record_vulnerability",
			want:      true,
		},
		{
			name:      "empty tool name against non-empty whitelist is denied",
			roleTools: []string{"nmap"},
			tool:      "",
			want:      false,
		},
	}

	o := &orchestratorState{}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := o.isToolAllowed(tc.tool, tc.roleTools)
			if got != tc.want {
				t.Fatalf("isToolAllowed(%q, %v) = %v, want %v", tc.tool, tc.roleTools, got, tc.want)
			}
		})
	}
}

// TestHandleWriteTodos_Valid checks the happy path: the tool stores the
// submitted todo list, returns a success string, and emits one "todos"
// progress event carrying the normalized list.
func TestHandleWriteTodos_Valid(t *testing.T) {
	t.Parallel()

	var gotEventType string
	var gotData interface{}
	progress := func(eventType, message string, data interface{}) {
		gotEventType = eventType
		gotData = data
	}
	o := &orchestratorState{
		progress:       progress,
		conversationID: "conv-123",
	}

	args := map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{"content": "Enumerate open ports", "status": "pending"},
			map[string]interface{}{"content": "Probe HTTP services", "status": "in_progress"},
		},
	}

	result, isErr := o.handleWriteTodos(args)
	if isErr {
		t.Fatalf("expected success, got error result: %q", result)
	}
	if !strings.Contains(result, "(2 items)") {
		t.Fatalf("expected item count in result, got %q", result)
	}

	o.mu.Lock()
	stored := o.todos
	o.mu.Unlock()
	if len(stored) != 2 {
		t.Fatalf("expected 2 todos stored, got %d", len(stored))
	}
	if stored[1].Status != "in_progress" {
		t.Fatalf("expected second todo status=in_progress, got %q", stored[1].Status)
	}

	if gotEventType != "todos" {
		t.Fatalf("expected progress event type %q, got %q", "todos", gotEventType)
	}
	m, ok := gotData.(map[string]interface{})
	if !ok {
		t.Fatalf("expected progress data to be a map, got %T", gotData)
	}
	if m["conversationId"] != "conv-123" {
		t.Fatalf("expected conversationId in progress data, got %v", m["conversationId"])
	}
}

// TestHandleWriteTodos_MissingField covers the error path where the LLM
// called write_todos but omitted the required todos array. The tool must
// return isError=true without mutating state.
func TestHandleWriteTodos_MissingField(t *testing.T) {
	t.Parallel()

	o := &orchestratorState{}
	result, isErr := o.handleWriteTodos(map[string]interface{}{})
	if !isErr {
		t.Fatalf("expected error, got success: %q", result)
	}
	if !strings.Contains(strings.ToLower(result), "required") {
		t.Fatalf("expected error message to mention 'required', got %q", result)
	}
	if len(o.todos) != 0 {
		t.Fatalf("expected todos unchanged on error, got %d items", len(o.todos))
	}
}

// TestHandleWriteTodos_MalformedType covers the case where todos is
// present but not the expected array-of-objects shape. The JSON
// unmarshal should reject it and we must not crash or partially update
// state.
func TestHandleWriteTodos_MalformedType(t *testing.T) {
	t.Parallel()

	o := &orchestratorState{
		todos: []todoItem{{Content: "existing", Status: "pending"}},
	}
	args := map[string]interface{}{"todos": "not-an-array"}

	result, isErr := o.handleWriteTodos(args)
	if !isErr {
		t.Fatalf("expected error, got success: %q", result)
	}
	if len(o.todos) != 1 || o.todos[0].Content != "existing" {
		t.Fatalf("expected existing todos preserved on error, got %+v", o.todos)
	}
}

// TestHandleWriteTodos_EmptyList accepts an explicitly empty list and
// replaces any prior state — the LLM may clear its plan by submitting
// an empty array.
func TestHandleWriteTodos_EmptyList(t *testing.T) {
	t.Parallel()

	o := &orchestratorState{
		todos: []todoItem{{Content: "stale plan", Status: "pending"}},
	}
	args := map[string]interface{}{"todos": []interface{}{}}

	result, isErr := o.handleWriteTodos(args)
	if isErr {
		t.Fatalf("expected success on empty list, got error: %q", result)
	}
	if len(o.todos) != 0 {
		t.Fatalf("expected todos cleared, got %d items", len(o.todos))
	}
}

// TestHandleWriteTodos_JSONRoundtrip confirms that whatever shape the
// LLM submits (as it comes out of the OpenAI tool-args JSON decode) is
// accepted, and that the stored todoItem slice is JSON-encodable back.
// This guards against a refactor accidentally introducing a field type
// that can't round-trip through the SSE data payload.
func TestHandleWriteTodos_JSONRoundtrip(t *testing.T) {
	t.Parallel()

	var captured interface{}
	o := &orchestratorState{
		progress: func(eventType, message string, data interface{}) {
			captured = data
		},
	}

	args := map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{"content": "task A", "status": "pending"},
		},
	}
	if _, isErr := o.handleWriteTodos(args); isErr {
		t.Fatalf("unexpected error")
	}

	if _, err := json.Marshal(captured); err != nil {
		t.Fatalf("progress data is not JSON-encodable: %v", err)
	}
}

// TestSnapshotMCPIDs_ConcurrentWrites confirms the mcpIDs mutex works
// under concurrent recordMCPID calls. Relevant now that #30 removed
// the broken save-swap-restore pattern — the orchestrator's own
// concurrency primitives need to stay correct.
func TestSnapshotMCPIDs_ConcurrentWrites(t *testing.T) {
	t.Parallel()

	o := &orchestratorState{}
	var wg sync.WaitGroup
	const workers = 50
	const perWorker = 20

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				o.recordMCPID("exec-id")
			}
		}()
	}
	wg.Wait()

	snap := o.snapshotMCPIDs()
	if len(snap) != workers*perWorker {
		t.Fatalf("expected %d recorded ids, got %d", workers*perWorker, len(snap))
	}
}

func openOrchestratorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "orch_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ddl := []string{
		`CREATE TABLE debug_sessions (conversation_id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT, label TEXT)`,
		`CREATE TABLE debug_events (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, message_id TEXT, seq INTEGER NOT NULL, event_type TEXT NOT NULL, agent_id TEXT, payload_json TEXT NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER)`,
		`CREATE TABLE debug_llm_calls (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, message_id TEXT, iteration INTEGER, call_index INTEGER, agent_id TEXT, sent_at INTEGER NOT NULL, first_token_at INTEGER, finished_at INTEGER, prompt_tokens INTEGER, completion_tokens INTEGER, request_json TEXT NOT NULL, response_json TEXT NOT NULL, error TEXT)`,
	}
	for _, s := range ddl {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("Exec: %v (%s)", err, s)
		}
	}
	return db
}

func TestOrchestrator_sendProgress_TeesToSink(t *testing.T) {
	db := openOrchestratorTestDB(t)
	sink := debug.NewSink(true, db, nil)
	o := &orchestratorState{
		ctx:            context.Background(),
		conversationID: "conv-t",
		sink:           sink,
		progress:       func(string, string, interface{}) {},
	}

	o.sendProgress("iteration", "", map[string]interface{}{
		"iteration":      1,
		"agent":          "cyberstrike-orchestrator",
		"conversationId": "conv-t",
	})

	var n int
	var evType, payload string
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(event_type),''), COALESCE(MAX(payload_json),'') FROM debug_events WHERE conversation_id = ?`, "conv-t").Scan(&n, &evType, &payload)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 debug_events row, got %d", n)
	}
	if evType != "iteration" {
		t.Fatalf("event_type: want iteration, got %q", evType)
	}
	if !strings.Contains(payload, `"iteration":1`) {
		t.Fatalf("payload missing iteration field: %s", payload)
	}
}

func TestOrchestrator_sendProgress_ExtractsAgentIDFromData(t *testing.T) {
	db := openOrchestratorTestDB(t)
	sink := debug.NewSink(true, db, nil)
	o := &orchestratorState{
		ctx:            context.Background(),
		conversationID: "conv-t",
		sink:           sink,
		progress:       func(string, string, interface{}) {},
	}

	// Event with an explicit agent field should store that agent_id.
	o.sendProgress("subagent_reply", "done", map[string]interface{}{
		"agent":          "recon-subagent",
		"conversationId": "conv-t",
	})
	// Event without an agent field should default to the orchestrator.
	o.sendProgress("tool_calls_detected", "2 calls", map[string]interface{}{
		"count": 2,
	})

	rows, err := db.Query(`SELECT event_type, agent_id FROM debug_events WHERE conversation_id = ? ORDER BY seq`, "conv-t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	type pair struct{ ev, agent string }
	var got []pair
	for rows.Next() {
		var ev, agent sql.NullString
		if err := rows.Scan(&ev, &agent); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, pair{ev.String, agent.String})
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if got[0].ev != "subagent_reply" || got[0].agent != "recon-subagent" {
		t.Fatalf("row 0: want (subagent_reply, recon-subagent), got %+v", got[0])
	}
	if got[1].ev != "tool_calls_detected" || got[1].agent != "cyberstrike-orchestrator" {
		t.Fatalf("row 1: want (tool_calls_detected, cyberstrike-orchestrator), got %+v", got[1])
	}
}

func TestOrchestrator_sendProgress_NoopSinkDoesNothing(t *testing.T) {
	db := openOrchestratorTestDB(t)
	sink := debug.NewSink(false, nil, nil) // noop
	o := &orchestratorState{
		ctx:            context.Background(),
		conversationID: "conv-t",
		sink:           sink,
		progress:       func(string, string, interface{}) {},
	}
	o.sendProgress("iteration", "", map[string]interface{}{"iteration": 1})
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM debug_events").Scan(&n)
	if n != 0 {
		t.Fatalf("noop sink should write zero rows, got %d", n)
	}
}
