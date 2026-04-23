package debug

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPruneOnce_DeletesOnlyOldSessions(t *testing.T) {
	db := openTestDB(t)
	nowNS := time.Now().UnixNano()
	thirtyDaysAgoNS := nowNS - int64(30*24*time.Hour)

	mustExec := func(q string, args ...interface{}) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// Old session (30 days old, 7-day retention → must prune).
	mustExec(`INSERT INTO debug_sessions (conversation_id, started_at, ended_at, outcome) VALUES ('old', ?, ?, 'completed')`, thirtyDaysAgoNS, thirtyDaysAgoNS+1)
	mustExec(`INSERT INTO debug_events (conversation_id, seq, event_type, payload_json, started_at) VALUES ('old', 0, 'iteration', '{}', ?)`, thirtyDaysAgoNS)
	mustExec(`INSERT INTO debug_llm_calls (conversation_id, sent_at, request_json, response_json) VALUES ('old', ?, '{}', '{}')`, thirtyDaysAgoNS)

	// Fresh session (must survive).
	mustExec(`INSERT INTO debug_sessions (conversation_id, started_at, ended_at, outcome) VALUES ('fresh', ?, ?, 'completed')`, nowNS, nowNS+1)
	mustExec(`INSERT INTO debug_events (conversation_id, seq, event_type, payload_json, started_at) VALUES ('fresh', 0, 'iteration', '{}', ?)`, nowNS)

	// Live session (ended_at NULL) must never be pruned regardless of age.
	mustExec(`INSERT INTO debug_sessions (conversation_id, started_at) VALUES ('live', ?)`, thirtyDaysAgoNS)

	if err := PruneOnce(db, 7, zap.NewNop()); err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}

	// Old session: rows gone from all three tables.
	for _, tbl := range []string{"debug_sessions", "debug_events", "debug_llm_calls"} {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE conversation_id = 'old'`).Scan(&n)
		if n != 0 {
			t.Fatalf("%s still has 'old' rows after prune: %d", tbl, n)
		}
	}
	// Fresh and live sessions survive.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM debug_sessions WHERE conversation_id IN ('fresh','live')`).Scan(&n)
	if n != 2 {
		t.Fatalf("fresh + live sessions should survive: got %d", n)
	}
}

func TestPruneOnce_NoOpWhenRetainDaysZero(t *testing.T) {
	db := openTestDB(t)
	thirtyDaysAgoNS := time.Now().UnixNano() - int64(30*24*time.Hour)
	if _, err := db.Exec(`INSERT INTO debug_sessions (conversation_id, started_at, ended_at, outcome) VALUES ('ancient', ?, ?, 'completed')`, thirtyDaysAgoNS, thirtyDaysAgoNS+1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := PruneOnce(db, 0, zap.NewNop()); err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM debug_sessions WHERE conversation_id = 'ancient'`).Scan(&n)
	if n != 1 {
		t.Fatalf("retain_days=0 should keep forever: ancient row gone")
	}
}
