package debug

import (
	"context"
	"database/sql"
	"time"

	"go.uber.org/zap"
)

// PruneOnce deletes debug rows for sessions whose ended_at is older
// than retainDays * 24h. Live sessions (ended_at IS NULL) are never
// pruned — they belong to a currently-running conversation or are
// orphans waiting for the next boot sweep.
//
// Runs inside one transaction so a partial failure can't leave orphan
// child rows in debug_events / debug_llm_calls.
//
// retainDays <= 0 is a no-op, matching the config semantic
// "0 = keep forever".
func PruneOnce(db *sql.DB, retainDays int, log *zap.Logger) error {
	if db == nil || retainDays <= 0 {
		return nil
	}
	if log == nil {
		log = zap.NewNop()
	}
	cutoffNS := time.Now().UnixNano() - int64(retainDays)*int64(24*time.Hour)
	tx, err := db.Begin()
	if err != nil {
		log.Warn("debug: retention begin failed", zap.Error(err))
		return err
	}
	defer tx.Rollback()

	for _, tbl := range []string{"debug_events", "debug_llm_calls"} {
		_, err := tx.Exec(`
			DELETE FROM `+tbl+`
			WHERE conversation_id IN (
			    SELECT conversation_id FROM debug_sessions
			    WHERE ended_at IS NOT NULL AND ended_at < ?
			)`, cutoffNS)
		if err != nil {
			log.Warn("debug: retention delete failed", zap.String("table", tbl), zap.Error(err))
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM debug_sessions WHERE ended_at IS NOT NULL AND ended_at < ?`, cutoffNS); err != nil {
		log.Warn("debug: retention delete sessions failed", zap.Error(err))
		return err
	}
	return tx.Commit()
}

// StartRetentionWorker runs PruneOnce on start, then every interval
// (default 24h) until ctx cancels. Wired in cmd/server/main.go via
// Task 23.
func StartRetentionWorker(ctx context.Context, db *sql.DB, retainDays int, interval time.Duration, log *zap.Logger) {
	if db == nil || retainDays <= 0 {
		return
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if log == nil {
		log = zap.NewNop()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	_ = PruneOnce(db, retainDays, log)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = PruneOnce(db, retainDays, log)
		}
	}
}
