package debug

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
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

// openTestDB opens an in-memory SQLite and runs the debug-table DDL
// so tests don't depend on the database package.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "debug_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ddl := []string{
		`CREATE TABLE debug_sessions (conversation_id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT, label TEXT)`,
		`CREATE TABLE debug_llm_calls (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, message_id TEXT, iteration INTEGER, call_index INTEGER, agent_id TEXT, sent_at INTEGER NOT NULL, first_token_at INTEGER, finished_at INTEGER, prompt_tokens INTEGER, completion_tokens INTEGER, request_json TEXT NOT NULL, response_json TEXT NOT NULL, error TEXT)`,
		`CREATE TABLE debug_events (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, message_id TEXT, seq INTEGER NOT NULL, event_type TEXT NOT NULL, agent_id TEXT, payload_json TEXT NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER)`,
	}
	for _, s := range ddl {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("Exec: %v (%s)", err, s)
		}
	}
	return db
}

func TestDBSink_StartEndSession_HappyPath(t *testing.T) {
	db := openTestDB(t)
	s := NewSink(true, db, nil)

	s.StartSession("conv-1")
	time.Sleep(2 * time.Millisecond) // ensure ended_at > started_at
	s.EndSession("conv-1", "completed")

	var startedAt, endedAt sql.NullInt64
	var outcome sql.NullString
	err := db.QueryRow("SELECT started_at, ended_at, outcome FROM debug_sessions WHERE conversation_id = ?", "conv-1").
		Scan(&startedAt, &endedAt, &outcome)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if !startedAt.Valid || startedAt.Int64 == 0 {
		t.Fatalf("started_at not populated")
	}
	if !endedAt.Valid || endedAt.Int64 <= startedAt.Int64 {
		t.Fatalf("ended_at not after started_at: start=%d end=%v", startedAt.Int64, endedAt)
	}
	if outcome.String != "completed" {
		t.Fatalf("outcome: want completed, got %q", outcome.String)
	}
}
