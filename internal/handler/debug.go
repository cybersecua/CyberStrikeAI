package handler

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// DebugHandler hosts the /api/debug/* routes (Tasks 15-18).
type DebugHandler struct {
	db     *sql.DB
	logger *zap.Logger
}

// NewDebugHandler constructs the handler. Nil logger defaults to nop.
func NewDebugHandler(db *sql.DB, logger *zap.Logger) *DebugHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DebugHandler{db: db, logger: logger}
}

// sessionSummary is the per-row shape returned by ListSessions.
type sessionSummary struct {
	ConversationID   string `json:"conversationId"`
	Label            string `json:"label,omitempty"`
	StartedAt        int64  `json:"startedAt"`
	EndedAt          *int64 `json:"endedAt,omitempty"`
	Outcome          string `json:"outcome,omitempty"`
	Iterations       int64  `json:"iterations"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	DurationMs       int64  `json:"durationMs"`
}

// ListSessions handles GET /api/debug/sessions.
// Per-row aggregates (iterations, tokens, durationMs) are computed
// at query time via SQL aggregates over debug_llm_calls; they're
// not stored columns on debug_sessions. Empty result returns [] with
// 200, not 404.
func (h *DebugHandler) ListSessions(c *gin.Context) {
	rows, err := h.db.Query(`
		SELECT
		  s.conversation_id,
		  COALESCE(s.label, ''),
		  s.started_at,
		  s.ended_at,
		  COALESCE(s.outcome, ''),
		  COALESCE((SELECT COUNT(DISTINCT iteration) FROM debug_llm_calls WHERE conversation_id = s.conversation_id), 0) AS iterations,
		  COALESCE((SELECT SUM(prompt_tokens)        FROM debug_llm_calls WHERE conversation_id = s.conversation_id), 0) AS prompt_tokens,
		  COALESCE((SELECT SUM(completion_tokens)    FROM debug_llm_calls WHERE conversation_id = s.conversation_id), 0) AS completion_tokens
		FROM debug_sessions s
		ORDER BY s.started_at DESC
		LIMIT 200
	`)
	if err != nil {
		h.logger.Warn("debug: ListSessions query failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "debug query failed"})
		return
	}
	defer rows.Close()

	out := make([]sessionSummary, 0, 16)
	for rows.Next() {
		var r sessionSummary
		var endedAt sql.NullInt64
		if err := rows.Scan(&r.ConversationID, &r.Label, &r.StartedAt, &endedAt, &r.Outcome, &r.Iterations, &r.PromptTokens, &r.CompletionTokens); err != nil {
			h.logger.Warn("debug: ListSessions scan failed", zap.Error(err))
			continue
		}
		if endedAt.Valid {
			r.EndedAt = &endedAt.Int64
			durationNs := endedAt.Int64 - r.StartedAt
			r.DurationMs = durationNs / 1_000_000
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, out)
}
