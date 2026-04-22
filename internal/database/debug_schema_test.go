package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

func TestInit_CreatesDebugTables(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Create a Database struct with a test logger
	logger := zap.NewNop()
	d := &DB{DB: db, logger: logger}
	if err := d.initTables(); err != nil {
		t.Fatalf("initTables: %v", err)
	}

	for _, table := range []string{"debug_sessions", "debug_llm_calls", "debug_events"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	for _, idx := range []string{"idx_debug_llm_calls_conv", "idx_debug_events_conv"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&name)
		if err != nil {
			t.Fatalf("index %s missing: %v", idx, err)
		}
	}
}
