package agent

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MemoryCategory defines the type of memory entry.
type MemoryCategory string

const (
	// MemoryCategoryCredential stores discovered credentials, tokens, and secrets.
	MemoryCategoryCredential MemoryCategory = "credential"
	// MemoryCategoryTarget stores information about targets (IPs, domains, services).
	MemoryCategoryTarget MemoryCategory = "target"
	// MemoryCategoryVulnerability stores vulnerability notes and exploit details.
	MemoryCategoryVulnerability MemoryCategory = "vulnerability"
	// MemoryCategoryFact stores general facts and observations.
	MemoryCategoryFact MemoryCategory = "fact"
	// MemoryCategoryNote stores operational notes and planning reminders.
	MemoryCategoryNote MemoryCategory = "note"
	// MemoryCategoryToolRun stores records of completed tool executions to prevent duplicate work.
	MemoryCategoryToolRun MemoryCategory = "tool_run"
	// MemoryCategoryDiscovery stores discoveries that need further investigation or classification.
	MemoryCategoryDiscovery MemoryCategory = "discovery"
	// MemoryCategoryPlan stores action plans and their step completion status.
	MemoryCategoryPlan MemoryCategory = "plan"
)

// MemoryStatus represents the validation state of a memory entry.
type MemoryStatus string

const (
	// MemoryStatusActive is the default state for newly stored memories.
	MemoryStatusActive MemoryStatus = "active"
	// MemoryStatusConfirmed means the finding has been validated and reproduced.
	MemoryStatusConfirmed MemoryStatus = "confirmed"
	// MemoryStatusFalsePositive means the finding was investigated and ruled out.
	MemoryStatusFalsePositive MemoryStatus = "false_positive"
	// MemoryStatusDisproven means the fact was found to be incorrect after further investigation.
	MemoryStatusDisproven MemoryStatus = "disproven"
)

// MemoryConfidence represents how certain the agent is about a memory entry.
type MemoryConfidence string

const (
	MemoryConfidenceHigh   MemoryConfidence = "high"
	MemoryConfidenceMedium MemoryConfidence = "medium"
	MemoryConfidenceLow    MemoryConfidence = "low"
)

// MemoryEntry represents a single persistent memory item.
type MemoryEntry struct {
	ID             string           `json:"id"`
	Key            string           `json:"key"`
	Value          string           `json:"value"`
	Category       MemoryCategory   `json:"category"`
	Status         MemoryStatus     `json:"status"`
	Entity         string           `json:"entity,omitempty"`
	Confidence     MemoryConfidence `json:"confidence"`
	ConversationID string           `json:"conversation_id,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// PersistentMemory manages long-lived memory entries that survive conversation
// compression and persist across sessions. Entries are stored in SQLite and
// injected as a context block in every agent system prompt.
type PersistentMemory struct {
	db     *sql.DB
	mu     sync.RWMutex
	logger *zap.Logger
}

// NewPersistentMemory creates a PersistentMemory backed by the given SQLite DB.
// It runs the table migration on first call so existing databases are safe.
func NewPersistentMemory(db *sql.DB, logger *zap.Logger) (*PersistentMemory, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	pm := &PersistentMemory{db: db, logger: logger}
	if err := pm.migrate(); err != nil {
		return nil, fmt.Errorf("persistent memory migration failed: %w", err)
	}
	return pm, nil
}

// migrate creates the agent_memories table if it does not exist and adds any
// missing columns for schema upgrades (idempotent).
func (pm *PersistentMemory) migrate() error {
	createTable := `
	CREATE TABLE IF NOT EXISTS agent_memories (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT 'fact',
		conversation_id TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_agent_memories_category ON agent_memories(category);
	CREATE INDEX IF NOT EXISTS idx_agent_memories_key ON agent_memories(key);
	CREATE INDEX IF NOT EXISTS idx_agent_memories_conversation ON agent_memories(conversation_id);
	`
	if _, err := pm.db.Exec(createTable); err != nil {
		return err
	}

	// Add new columns if they don't exist yet (safe for existing databases).
	alterStatements := []string{
		`ALTER TABLE agent_memories ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,
		`ALTER TABLE agent_memories ADD COLUMN entity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_memories ADD COLUMN confidence TEXT NOT NULL DEFAULT 'medium'`,
	}
	for _, stmt := range alterStatements {
		_, err := pm.db.Exec(stmt)
		if err != nil {
			// SQLite returns an error if column already exists; ignore it.
			if !strings.Contains(err.Error(), "duplicate column name") {
				pm.logger.Warn("schema migration warning", zap.String("stmt", stmt), zap.Error(err))
			}
		}
	}

	// Add indexes for new columns.
	indexStatements := []string{
		`CREATE INDEX IF NOT EXISTS idx_agent_memories_status ON agent_memories(status)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_memories_entity ON agent_memories(entity)`,
	}
	for _, stmt := range indexStatements {
		if _, err := pm.db.Exec(stmt); err != nil {
			pm.logger.Warn("index creation warning", zap.String("stmt", stmt), zap.Error(err))
		}
	}

	return nil
}

// Store upserts a memory entry by key. If a record with the same key already
// exists it is updated in-place; otherwise a new entry is inserted.
// Status defaults to "active" and confidence to "medium".
func (pm *PersistentMemory) Store(key, value string, category MemoryCategory, conversationID string) (*MemoryEntry, error) {
	return pm.StoreFull(key, value, category, conversationID, "", MemoryConfidenceMedium, MemoryStatusActive)
}

// StoreFull upserts a memory entry with full metadata. Use this when entity,
// confidence, or status need to be set explicitly.
func (pm *PersistentMemory) StoreFull(key, value string, category MemoryCategory, conversationID, entity string, confidence MemoryConfidence, status MemoryStatus) (*MemoryEntry, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if confidence == "" {
		confidence = MemoryConfidenceMedium
	}
	if status == "" {
		status = MemoryStatusActive
	}

	now := time.Now().UTC()

	// Check for existing entry with same key.
	var existingID string
	err := pm.db.QueryRow("SELECT id FROM agent_memories WHERE key = ?", key).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query existing memory: %w", err)
	}

	if existingID != "" {
		// Update existing entry.
		_, err = pm.db.Exec(
			"UPDATE agent_memories SET value = ?, category = ?, conversation_id = ?, entity = ?, confidence = ?, status = ?, updated_at = ? WHERE id = ?",
			value, string(category), conversationID, entity, string(confidence), string(status), now, existingID,
		)
		if err != nil {
			return nil, fmt.Errorf("update memory: %w", err)
		}
		pm.logger.Debug("updated persistent memory", zap.String("key", key), zap.String("category", string(category)), zap.String("entity", entity))
		return &MemoryEntry{
			ID:             existingID,
			Key:            key,
			Value:          value,
			Category:       category,
			Status:         status,
			Entity:         entity,
			Confidence:     confidence,
			ConversationID: conversationID,
			UpdatedAt:      now,
		}, nil
	}

	// Insert new entry.
	id := uuid.New().String()
	_, err = pm.db.Exec(
		"INSERT INTO agent_memories (id, key, value, category, conversation_id, entity, confidence, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, key, value, string(category), conversationID, entity, string(confidence), string(status), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}
	pm.logger.Debug("stored new persistent memory", zap.String("key", key), zap.String("category", string(category)), zap.String("entity", entity))
	return &MemoryEntry{
		ID:             id,
		Key:            key,
		Value:          value,
		Category:       category,
		Status:         status,
		Entity:         entity,
		Confidence:     confidence,
		ConversationID: conversationID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// SetStatus updates the status of an existing memory entry by ID.
// Use this to mark findings as confirmed, false_positive, or disproven.
func (pm *PersistentMemory) SetStatus(id string, status MemoryStatus) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now().UTC()
	res, err := pm.db.Exec(
		"UPDATE agent_memories SET status = ?, updated_at = ? WHERE id = ?",
		string(status), now, id,
	)
	if err != nil {
		return fmt.Errorf("set memory status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory entry not found: %s", id)
	}
	pm.logger.Debug("memory status updated", zap.String("id", id), zap.String("status", string(status)))
	return nil
}

// Retrieve fetches memory entries matching the query. The query is matched
// case-insensitively against both the key and value fields. If category is
// non-empty only entries of that category are returned.
// Disproven and false_positive entries are excluded by default.
func (pm *PersistentMemory) Retrieve(query string, category MemoryCategory, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	var (
		rows *sql.Rows
		err  error
	)

	likeQ := "%" + strings.ToLower(query) + "%"
	// Exclude disproven/false_positive by default so searches return actionable data.
	excludeStatuses := []interface{}{string(MemoryStatusDisproven), string(MemoryStatusFalsePositive)}

	if category != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE category = ? AND (LOWER(key) LIKE ? OR LOWER(value) LIKE ?)
			   AND status NOT IN (?, ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			append([]interface{}{string(category), likeQ, likeQ}, append(excludeStatuses, limit)...)...,
		)
	} else if query != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE (LOWER(key) LIKE ? OR LOWER(value) LIKE ?)
			   AND status NOT IN (?, ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			append([]interface{}{likeQ, likeQ}, append(excludeStatuses, limit)...)...,
		)
	} else {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE status NOT IN (?, ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			append(excludeStatuses, limit)...,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// RetrieveAll is like Retrieve but includes disproven and false_positive entries.
// Useful for auditing or when the model explicitly wants to review dismissed findings.
func (pm *PersistentMemory) RetrieveAll(query string, category MemoryCategory, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	var (
		rows *sql.Rows
		err  error
	)

	likeQ := "%" + strings.ToLower(query) + "%"

	if category != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE category = ? AND (LOWER(key) LIKE ? OR LOWER(value) LIKE ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			string(category), likeQ, likeQ, limit,
		)
	} else if query != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE LOWER(key) LIKE ? OR LOWER(value) LIKE ?
			 ORDER BY updated_at DESC LIMIT ?`,
			likeQ, likeQ, limit,
		)
	} else {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 ORDER BY updated_at DESC LIMIT ?`,
			limit,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("query all memories: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// List returns all memory entries optionally filtered by category, ordered by
// most recently updated first. Disproven and false_positive entries are excluded.
func (pm *PersistentMemory) List(category MemoryCategory, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	var (
		rows *sql.Rows
		err  error
	)

	excludeStatuses := []interface{}{string(MemoryStatusDisproven), string(MemoryStatusFalsePositive)}

	if category != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE category = ? AND status NOT IN (?, ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			append([]interface{}{string(category)}, append(excludeStatuses, limit)...)...,
		)
	} else {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories
			 WHERE status NOT IN (?, ?)
			 ORDER BY updated_at DESC LIMIT ?`,
			append(excludeStatuses, limit)...,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// ListAll returns all memory entries including disproven/false_positive ones.
func (pm *PersistentMemory) ListAll(category MemoryCategory, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	var (
		rows *sql.Rows
		err  error
	)

	if category != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories WHERE category = ? ORDER BY updated_at DESC LIMIT ?`,
			string(category), limit,
		)
	} else {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories ORDER BY updated_at DESC LIMIT ?`,
			limit,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("list all memories: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// ListByEntity returns all active memories associated with a given entity
// (e.g., a target hostname, IP address, or service name).
func (pm *PersistentMemory) ListByEntity(entity string, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := pm.db.Query(
		`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
		 FROM agent_memories
		 WHERE entity = ? AND status NOT IN (?, ?)
		 ORDER BY updated_at DESC LIMIT ?`,
		entity, string(MemoryStatusDisproven), string(MemoryStatusFalsePositive), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list by entity: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// FindByStatus returns memories matching a specific status, optionally filtered by category.
func (pm *PersistentMemory) FindByStatus(status MemoryStatus, category MemoryCategory, limit int) ([]*MemoryEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	var (
		rows *sql.Rows
		err  error
	)

	if category != "" {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories WHERE status = ? AND category = ? ORDER BY updated_at DESC LIMIT ?`,
			string(status), string(category), limit,
		)
	} else {
		rows, err = pm.db.Query(
			`SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at
			 FROM agent_memories WHERE status = ? ORDER BY updated_at DESC LIMIT ?`,
			string(status), limit,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("find by status: %w", err)
	}
	defer rows.Close()

	return pm.scanRows(rows)
}

// UpdateByID updates the key, value, and category of an existing memory entry
// identified by its UUID. Returns an error if the entry is not found.
func (pm *PersistentMemory) UpdateByID(id, key, value string, category MemoryCategory) (*MemoryEntry, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now().UTC()

	res, err := pm.db.Exec(
		"UPDATE agent_memories SET key = ?, value = ?, category = ?, updated_at = ? WHERE id = ?",
		key, value, string(category), now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update memory by id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("memory entry not found: %s", id)
	}

	pm.logger.Debug("updated persistent memory by id", zap.String("id", id), zap.String("key", key))

	var e MemoryEntry
	var convID sql.NullString
	var entityVal, confidenceVal, statusVal sql.NullString
	var createdAtStr, updatedAtStr string
	err = pm.db.QueryRow(
		"SELECT id, key, value, category, status, entity, confidence, conversation_id, created_at, updated_at FROM agent_memories WHERE id = ?", id,
	).Scan(&e.ID, &e.Key, &e.Value, &e.Category, &statusVal, &entityVal, &confidenceVal, &convID, &createdAtStr, &updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("fetch updated memory: %w", err)
	}
	if convID.Valid {
		e.ConversationID = convID.String
	}
	if entityVal.Valid {
		e.Entity = entityVal.String
	}
	if confidenceVal.Valid {
		e.Confidence = MemoryConfidence(confidenceVal.String)
	}
	if statusVal.Valid {
		e.Status = MemoryStatus(statusVal.String)
	}
	if e.Status == "" {
		e.Status = MemoryStatusActive
	}
	if e.Confidence == "" {
		e.Confidence = MemoryConfidenceMedium
	}
	if t, parseErr := time.Parse(time.RFC3339Nano, createdAtStr); parseErr == nil {
		e.CreatedAt = t
	}
	if t, parseErr := time.Parse(time.RFC3339Nano, updatedAtStr); parseErr == nil {
		e.UpdatedAt = t
	}
	return &e, nil
}

// Delete removes a memory entry by ID. Returns nil if the entry did not exist.
func (pm *PersistentMemory) Delete(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	res, err := pm.db.Exec("DELETE FROM agent_memories WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		pm.logger.Debug("delete memory: entry not found", zap.String("id", id))
	}
	return nil
}

// BuildContextBlock returns a structured text summary of all stored memories
// suitable for injection into a system prompt. It organizes memories by category
// and entity, highlights status, and separates dismissed findings from active ones.
// Returns an empty string when there are no memories.
func (pm *PersistentMemory) BuildContextBlock() string {
	entries, err := pm.ListAll("", 200)
	if err != nil || len(entries) == 0 {
		return ""
	}

	// Separate active/confirmed from dismissed entries.
	var active, dismissed []*MemoryEntry
	for _, e := range entries {
		switch e.Status {
		case MemoryStatusDisproven, MemoryStatusFalsePositive:
			dismissed = append(dismissed, e)
		default:
			active = append(active, e)
		}
	}

	if len(active) == 0 && len(dismissed) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<persistent_memory>\n")
	sb.WriteString("The following facts were remembered from previous sessions or earlier in this session:\n")

	// ── Active / Confirmed entries ──────────────────────────────────────────
	// Primary categories to show prominently.
	primaryCategories := []MemoryCategory{
		MemoryCategoryCredential,
		MemoryCategoryTarget,
		MemoryCategoryVulnerability,
		MemoryCategoryDiscovery,
		MemoryCategoryFact,
		MemoryCategoryNote,
		MemoryCategoryPlan,
	}

	byCategory := make(map[MemoryCategory][]*MemoryEntry)
	byEntityActive := make(map[string][]*MemoryEntry)
	toolRunEntries := []*MemoryEntry{}

	for _, e := range active {
		if e.Category == MemoryCategoryToolRun {
			toolRunEntries = append(toolRunEntries, e)
			continue
		}
		byCategory[e.Category] = append(byCategory[e.Category], e)
		if e.Entity != "" {
			byEntityActive[e.Entity] = append(byEntityActive[e.Entity], e)
		}
	}

	for _, cat := range primaryCategories {
		items := byCategory[cat]
		if len(items) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n[%s]\n", strings.ToUpper(string(cat))))
		for _, item := range items {
			statusTag := ""
			if item.Status == MemoryStatusConfirmed {
				statusTag = " ✓"
			}
			confidenceTag := ""
			if item.Confidence == MemoryConfidenceLow {
				confidenceTag = " (low-confidence)"
			}
			entityTag := ""
			if item.Entity != "" {
				entityTag = fmt.Sprintf(" [entity:%s]", item.Entity)
			}
			sb.WriteString(fmt.Sprintf("  • %s: %s%s%s%s  (id:%s)\n",
				item.Key, item.Value, statusTag, confidenceTag, entityTag, item.ID))
		}
	}

	// ── Tool runs: compact list to prevent repeated execution ───────────────
	if len(toolRunEntries) > 0 {
		sb.WriteString("\n[COMPLETED TOOL RUNS — do not repeat these unless necessary]\n")
		for _, item := range toolRunEntries {
			sb.WriteString(fmt.Sprintf("  • %s: %s  (id:%s)\n", item.Key, item.Value, item.ID))
		}
	}

	// ── Dismissed / disproven findings ──────────────────────────────────────
	if len(dismissed) > 0 {
		sb.WriteString("\n[DISMISSED FINDINGS — false positives and disproven entries, do not re-investigate]\n")
		for _, item := range dismissed {
			statusLabel := "disproven"
			if item.Status == MemoryStatusFalsePositive {
				statusLabel = "false-positive"
			}
			sb.WriteString(fmt.Sprintf("  ✗ [%s][%s] %s: %s  (id:%s)\n",
				statusLabel, string(item.Category), item.Key, item.Value, item.ID))
		}
	}

	sb.WriteString("</persistent_memory>\n")
	return sb.String()
}

func (pm *PersistentMemory) scanRows(rows *sql.Rows) ([]*MemoryEntry, error) {
	var entries []*MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		var convID, entityVal, confidenceVal, statusVal sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&e.ID, &e.Key, &e.Value, &e.Category, &statusVal, &entityVal, &confidenceVal, &convID, &createdAt, &updatedAt); err != nil {
			pm.logger.Warn("scan memory row", zap.Error(err))
			continue
		}
		if convID.Valid {
			e.ConversationID = convID.String
		}
		if entityVal.Valid {
			e.Entity = entityVal.String
		}
		if confidenceVal.Valid {
			e.Confidence = MemoryConfidence(confidenceVal.String)
		} else {
			e.Confidence = MemoryConfidenceMedium
		}
		if statusVal.Valid && statusVal.String != "" {
			e.Status = MemoryStatus(statusVal.String)
		} else {
			e.Status = MemoryStatusActive
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			e.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			e.UpdatedAt = t
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
