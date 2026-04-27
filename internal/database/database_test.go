package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

func TestInitTables_BotSessionsAndPlatform(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "init_test.db")
	sqldb, err := sql.Open("sqlite3", "file:"+tmp+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqldb.Close()

	logger, _ := zap.NewDevelopment()
	d := &DB{DB: sqldb, logger: logger}
	if err := d.initTables(); err != nil {
		t.Fatalf("initTables: %v", err)
	}

	// bot_sessions table
	var name string
	if err := sqldb.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='bot_sessions'").Scan(&name); err != nil {
		t.Fatalf("bot_sessions table missing: %v", err)
	}

	// conversations.platform column
	rows, err := sqldb.Query(`PRAGMA table_info(conversations)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	hasPlatform := false
	for rows.Next() {
		var cid int
		var n, ttype string
		var notNull, pk int
		var dflt sql.NullString
		_ = rows.Scan(&cid, &n, &ttype, &notNull, &dflt, &pk)
		if n == "platform" {
			hasPlatform = true
		}
	}
	if !hasPlatform {
		t.Fatalf("conversations.platform column missing")
	}
}
