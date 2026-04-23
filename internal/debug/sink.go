package debug

import (
	"database/sql"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

// Sink is the debug-capture extension point. Every call site in the
// Agent / Orchestrator / Handler invokes a Sink unconditionally;
// noopSink short-circuits when debug is off, dbSink persists when on.
// The enabled flag lives on dbSink as an atomic.Bool and can be
// flipped at runtime by the Settings toggle endpoint.
type Sink interface {
	StartSession(conversationID string)
	EndSession(conversationID, outcome string)
	RecordLLMCall(conversationID, messageID string, c LLMCall)
	RecordEvent(conversationID, messageID string, e Event)
	SetEnabled(bool)
	Enabled() bool
}

// NewSink returns a dbSink when enabled at construction, otherwise a
// noopSink. The SetEnabled runtime toggle only flips writes for an
// already-dbSink; a noopSink stays a noopSink for the process lifetime
// (the handler wires a single Sink at boot).
func NewSink(enabled bool, db *sql.DB, log *zap.Logger) Sink {
	if log == nil {
		log = zap.NewNop()
	}
	if !enabled {
		return noopSink{}
	}
	s := &dbSink{db: db, log: log}
	s.enabled.Store(true)
	return s
}

// noopSink is the off-state sink. Every method returns immediately.
// Do not touch db; callers pass nil when debug is off.
type noopSink struct{}

func (noopSink) StartSession(string)                   {}
func (noopSink) EndSession(string, string)             {}
func (noopSink) RecordLLMCall(string, string, LLMCall) {}
func (noopSink) RecordEvent(string, string, Event)     {}
func (noopSink) SetEnabled(bool)                       {}
func (noopSink) Enabled() bool                         { return false }

// dbSink writes to SQLite. Writes are best-effort: any DB error is
// logged at warn and swallowed, so a debug-subsystem failure never
// takes down a user-facing conversation.
type dbSink struct {
	db      *sql.DB
	log     *zap.Logger
	enabled atomic.Bool

	// seqByConv[conversationID] is a *atomic.Int64 holding the next
	// event seq. Lazily populated on first RecordEvent for the
	// conversation; never deleted (bounded by retention sweep).
	seqByConv sync.Map
}

func (s *dbSink) SetEnabled(v bool) { s.enabled.Store(v) }
func (s *dbSink) Enabled() bool     { return s.enabled.Load() }

// RecordLLMCall and RecordEvent bodies are filled in Tasks 6-7.
func (s *dbSink) RecordLLMCall(conversationID, messageID string, c LLMCall) {}
func (s *dbSink) RecordEvent(conversationID, messageID string, e Event)     {}
