package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

func openConvTestDB(t *testing.T) *DB {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "conv_test.db")
	db, err := sql.Open("sqlite3", "file:"+tmp+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger, _ := zap.NewDevelopment()
	d := &DB{DB: db, logger: logger}
	if err := d.initTables(); err != nil {
		t.Fatalf("initTables: %v", err)
	}
	return d
}

func TestCreateConversationWithPlatform(t *testing.T) {
	d := openConvTestDB(t)
	conv, err := d.CreateConversationWithPlatform("scan", "telegram")
	if err != nil {
		t.Fatalf("CreateConversationWithPlatform: %v", err)
	}
	var platform sql.NullString
	if err := d.QueryRow(`SELECT platform FROM conversations WHERE id = ?`, conv.ID).Scan(&platform); err != nil {
		t.Fatalf("Query platform: %v", err)
	}
	if !platform.Valid || platform.String != "telegram" {
		t.Fatalf("platform: want 'telegram', got %v", platform)
	}
}

func TestCreateConversation_PlatformIsNULL(t *testing.T) {
	d := openConvTestDB(t)
	conv, err := d.CreateConversation("web chat")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	var platform sql.NullString
	_ = d.QueryRow(`SELECT platform FROM conversations WHERE id = ?`, conv.ID).Scan(&platform)
	if platform.Valid {
		t.Fatalf("expected NULL platform on web-origin conversation, got %v", platform)
	}
}

func TestListConversations_FilterByPlatform(t *testing.T) {
	d := openConvTestDB(t)
	_, _ = d.CreateConversation("web1")
	_, _ = d.CreateConversation("web2")
	_, _ = d.CreateConversationWithPlatform("tg1", "telegram")

	all, err := d.ListConversations(100, 0, "")
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all: want 3, got %d", len(all))
	}

	tgOnly, err := d.ListConversationsByPlatform(100, 0, "", "telegram")
	if err != nil {
		t.Fatalf("ListConversationsByPlatform: %v", err)
	}
	if len(tgOnly) != 1 || tgOnly[0].Title != "tg1" {
		t.Fatalf("tg-only: want [tg1], got %+v", tgOnly)
	}

	// Empty platform argument means "WHERE platform IS NULL" (web-only).
	webOnly, err := d.ListConversationsByPlatform(100, 0, "", "")
	if err != nil {
		t.Fatalf("ListConversationsByPlatform empty: %v", err)
	}
	if len(webOnly) != 2 {
		t.Fatalf("web-only: want 2, got %d", len(webOnly))
	}
}
