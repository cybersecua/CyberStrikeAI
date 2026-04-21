package filemanager

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// FileType classifies the kind of managed file.
type FileType string

const (
	FileTypeReport      FileType = "report"       // external tool output, scan results
	FileTypeAPIDocs     FileType = "api_docs"     // API documentation
	FileTypeProjectFile FileType = "project_file" // file from the project being worked on
	FileTypeTargetFile  FileType = "target_file"  // file obtained from target
	FileTypeReversing   FileType = "reversing"    // binary/file to reverse engineer
	FileTypeExfiltrated FileType = "exfiltrated"  // exfiltrated data
	FileTypeOther       FileType = "other"
)

// FileStatus tracks processing state.
type FileStatus string

const (
	FileStatusPending    FileStatus = "pending"     // uploaded, not yet processed
	FileStatusProcessing FileStatus = "processing"  // model is working on it
	FileStatusAnalyzed   FileStatus = "analyzed"    // initial analysis complete
	FileStatusInProgress FileStatus = "in_progress" // ongoing work
	FileStatusCompleted  FileStatus = "completed"   // work finished
	FileStatusArchived   FileStatus = "archived"
)

// ManagedFile represents a tracked file with its metadata and processing state.
type ManagedFile struct {
	ID             string     `json:"id"`
	FileName       string     `json:"file_name"`
	FilePath       string     `json:"file_path"`
	FileSize       int64      `json:"file_size"`
	MimeType       string     `json:"mime_type,omitempty"`
	FileType       FileType   `json:"file_type"`
	Status         FileStatus `json:"status"`
	Summary        string     `json:"summary"`        // model-generated summary of what the file is
	Location       string     `json:"location"`       // where the file is stored (relative path)
	HandlePlan     string     `json:"handle_plan"`    // how model plans to handle this file
	Progress       string     `json:"progress"`       // current progress notes
	Findings       string     `json:"findings"`       // discoveries/findings from processing
	Logs           string     `json:"logs"`           // processing log entries
	Tags           string     `json:"tags,omitempty"` // comma-separated tags
	ConversationID string     `json:"conversation_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Manager handles file storage, tracking, and metadata.
type Manager struct {
	db         *sql.DB
	storageDir string
	mu         sync.RWMutex
	logger     *zap.Logger
}

// NewManager creates a file manager backed by SQLite and a local storage directory.
func NewManager(db *sql.DB, storageDir string, logger *zap.Logger) (*Manager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// ensure storage directory exists
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return nil, fmt.Errorf("create file storage dir: %w", err)
	}

	m := &Manager{db: db, storageDir: storageDir, logger: logger}
	if err := m.migrate(); err != nil {
		return nil, fmt.Errorf("file manager migration: %w", err)
	}
	return m, nil
}

// StorageDir returns the base storage directory path.
func (m *Manager) StorageDir() string {
	return m.storageDir
}

func (m *Manager) migrate() error {
	createTable := `
	CREATE TABLE IF NOT EXISTS managed_files (
		id TEXT PRIMARY KEY,
		file_name TEXT NOT NULL,
		file_path TEXT NOT NULL,
		file_size INTEGER NOT NULL DEFAULT 0,
		mime_type TEXT,
		file_type TEXT NOT NULL DEFAULT 'other',
		status TEXT NOT NULL DEFAULT 'pending',
		summary TEXT NOT NULL DEFAULT '',
		location TEXT NOT NULL DEFAULT '',
		handle_plan TEXT NOT NULL DEFAULT '',
		progress TEXT NOT NULL DEFAULT '',
		findings TEXT NOT NULL DEFAULT '',
		logs TEXT NOT NULL DEFAULT '',
		tags TEXT NOT NULL DEFAULT '',
		conversation_id TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_managed_files_status ON managed_files(status);
	CREATE INDEX IF NOT EXISTS idx_managed_files_file_type ON managed_files(file_type);
	CREATE INDEX IF NOT EXISTS idx_managed_files_conversation ON managed_files(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_managed_files_created_at ON managed_files(created_at);
	`
	_, err := m.db.Exec(createTable)
	return err
}

// Register creates a new managed file entry from an already-saved file on disk.
func (m *Manager) Register(fileName, filePath string, fileSize int64, mimeType string, fileType FileType, conversationID string) (*ManagedFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	id := uuid.New().String()

	if fileType == "" {
		fileType = FileTypeOther
	}

	f := &ManagedFile{
		ID:             id,
		FileName:       fileName,
		FilePath:       filePath,
		FileSize:       fileSize,
		MimeType:       mimeType,
		FileType:       fileType,
		Status:         FileStatusPending,
		ConversationID: conversationID,
		Location:       filePath,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	_, err := m.db.Exec(
		`INSERT INTO managed_files (id, file_name, file_path, file_size, mime_type, file_type, status, summary, location, handle_plan, progress, findings, logs, tags, conversation_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, '', '', '', '', '', ?, ?, ?)`,
		id, fileName, filePath, fileSize, mimeType, string(fileType), string(FileStatusPending),
		filePath, conversationID, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert managed file: %w", err)
	}

	m.logger.Info("registered managed file", zap.String("id", id), zap.String("name", fileName))
	return f, nil
}

// Upload saves file content to the storage directory and registers it.
func (m *Manager) Upload(fileName string, content []byte, fileType FileType, conversationID string, mimeType string) (*ManagedFile, error) {
	// create subdirectory by type
	typeDir := filepath.Join(m.storageDir, string(fileType))
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		return nil, fmt.Errorf("create type dir: %w", err)
	}

	// unique filename
	ext := filepath.Ext(fileName)
	nameNoExt := strings.TrimSuffix(fileName, ext)
	unique := fmt.Sprintf("%s_%s%s", nameNoExt, uuid.New().String()[:8], ext)
	fullPath := filepath.Join(typeDir, unique)

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	absPath, _ := filepath.Abs(fullPath)
	return m.Register(fileName, absPath, int64(len(content)), mimeType, fileType, conversationID)
}

// Get returns a managed file by ID.
func (m *Manager) Get(id string) (*ManagedFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	row := m.db.QueryRow(
		`SELECT id, file_name, file_path, file_size, mime_type, file_type, status, summary, location, handle_plan, progress, findings, logs, tags, conversation_id, created_at, updated_at
		 FROM managed_files WHERE id = ?`, id,
	)
	return scanFile(row)
}

// List returns managed files with optional filtering.
func (m *Manager) List(fileType FileType, status FileStatus, search string, limit, offset int) ([]*ManagedFile, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var conditions []string
	var args []interface{}

	if fileType != "" {
		conditions = append(conditions, "file_type = ?")
		args = append(args, string(fileType))
	}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(status))
	}
	if search != "" {
		conditions = append(conditions, "(LOWER(file_name) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(findings) LIKE ? OR LOWER(tags) LIKE ?)")
		like := "%" + strings.ToLower(search) + "%"
		args = append(args, like, like, like, like)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// count total
	var total int
	countQuery := "SELECT COUNT(*) FROM managed_files " + where
	if err := m.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count files: %w", err)
	}

	// fetch page
	query := fmt.Sprintf(
		`SELECT id, file_name, file_path, file_size, mime_type, file_type, status, summary, location, handle_plan, progress, findings, logs, tags, conversation_id, created_at, updated_at
		 FROM managed_files %s ORDER BY updated_at DESC LIMIT ? OFFSET ?`, where,
	)
	args = append(args, limit, offset)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()

	var files []*ManagedFile
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			m.logger.Warn("scan managed file row", zap.Error(err))
			continue
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate files: %w", err)
	}

	return files, total, nil
}

// Update modifies a managed file's metadata fields.
func (m *Manager) Update(id string, updates map[string]interface{}) (*ManagedFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	allowedFields := map[string]bool{
		"summary": true, "handle_plan": true, "progress": true,
		"findings": true, "logs": true, "status": true,
		"file_type": true, "tags": true, "file_name": true,
	}

	var setClauses []string
	var args []interface{}
	for field, value := range updates {
		if !allowedFields[field] {
			continue
		}
		setClauses = append(setClauses, field+" = ?")
		args = append(args, value)
	}

	if len(setClauses) == 0 {
		return m.getUnlocked(id)
	}

	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, time.Now().UTC())
	args = append(args, id)

	query := fmt.Sprintf("UPDATE managed_files SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	res, err := m.db.Exec(query, args...)
	if err != nil {
		return nil, fmt.Errorf("update managed file: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("file not found: %s", id)
	}

	m.logger.Debug("updated managed file", zap.String("id", id))
	return m.getUnlocked(id)
}

// AppendLog appends a timestamped log entry to the file's log.
func (m *Manager) AppendLog(id, entry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	logLine := fmt.Sprintf("[%s] %s\n", now, entry)

	_, err := m.db.Exec(
		"UPDATE managed_files SET logs = logs || ?, updated_at = ? WHERE id = ?",
		logLine, time.Now().UTC(), id,
	)
	return err
}

// AppendFindings appends to findings.
func (m *Manager) AppendFindings(id, finding string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %s\n", now, finding)

	_, err := m.db.Exec(
		"UPDATE managed_files SET findings = findings || ?, updated_at = ? WHERE id = ?",
		line, time.Now().UTC(), id,
	)
	return err
}

// Delete removes a managed file entry and optionally its file on disk.
func (m *Manager) Delete(id string, deleteFromDisk bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if deleteFromDisk {
		var filePath string
		if err := m.db.QueryRow("SELECT file_path FROM managed_files WHERE id = ?", id).Scan(&filePath); err == nil && filePath != "" {
			_ = os.Remove(filePath)
		}
	}

	_, err := m.db.Exec("DELETE FROM managed_files WHERE id = ?", id)
	return err
}

// Stats returns aggregate counts by type and status.
func (m *Manager) Stats() (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var total int
	m.db.QueryRow("SELECT COUNT(*) FROM managed_files").Scan(&total)

	byType := make(map[string]int)
	rows, err := m.db.Query("SELECT file_type, COUNT(*) FROM managed_files GROUP BY file_type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ft string
			var count int
			rows.Scan(&ft, &count)
			byType[ft] = count
		}
	}

	byStatus := make(map[string]int)
	rows2, err := m.db.Query("SELECT status, COUNT(*) FROM managed_files GROUP BY status")
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var st string
			var count int
			rows2.Scan(&st, &count)
			byStatus[st] = count
		}
	}

	var totalSize int64
	m.db.QueryRow("SELECT COALESCE(SUM(file_size), 0) FROM managed_files").Scan(&totalSize)

	return map[string]interface{}{
		"total":      total,
		"total_size": totalSize,
		"by_type":    byType,
		"by_status":  byStatus,
	}, nil
}

// ReadFileContent reads the actual file content (for small text files).
func (m *Manager) ReadFileContent(id string, maxBytes int64) (string, error) {
	f, err := m.Get(id)
	if err != nil {
		return "", err
	}

	file, err := os.Open(f.FilePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	if maxBytes <= 0 {
		maxBytes = 100 * 1024 // 100KB default
	}

	buf := make([]byte, maxBytes)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read file: %w", err)
	}

	return string(buf[:n]), nil
}

func (m *Manager) getUnlocked(id string) (*ManagedFile, error) {
	row := m.db.QueryRow(
		`SELECT id, file_name, file_path, file_size, mime_type, file_type, status, summary, location, handle_plan, progress, findings, logs, tags, conversation_id, created_at, updated_at
		 FROM managed_files WHERE id = ?`, id,
	)
	return scanFile(row)
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanFile(row scanner) (*ManagedFile, error) {
	var f ManagedFile
	var mimeType, convID sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&f.ID, &f.FileName, &f.FilePath, &f.FileSize,
		&mimeType, &f.FileType, &f.Status,
		&f.Summary, &f.Location, &f.HandlePlan,
		&f.Progress, &f.Findings, &f.Logs, &f.Tags,
		&convID, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if mimeType.Valid {
		f.MimeType = mimeType.String
	}
	if convID.Valid {
		f.ConversationID = convID.String
	}
	if t, e := time.Parse(time.RFC3339Nano, createdAt); e == nil {
		f.CreatedAt = t
	}
	if t, e := time.Parse(time.RFC3339Nano, updatedAt); e == nil {
		f.UpdatedAt = t
	}
	return &f, nil
}

func scanFileRow(rows *sql.Rows) (*ManagedFile, error) {
	var f ManagedFile
	var mimeType, convID sql.NullString
	var createdAt, updatedAt string

	err := rows.Scan(
		&f.ID, &f.FileName, &f.FilePath, &f.FileSize,
		&mimeType, &f.FileType, &f.Status,
		&f.Summary, &f.Location, &f.HandlePlan,
		&f.Progress, &f.Findings, &f.Logs, &f.Tags,
		&convID, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if mimeType.Valid {
		f.MimeType = mimeType.String
	}
	if convID.Valid {
		f.ConversationID = convID.String
	}
	if t, e := time.Parse(time.RFC3339Nano, createdAt); e == nil {
		f.CreatedAt = t
	}
	if t, e := time.Parse(time.RFC3339Nano, updatedAt); e == nil {
		f.UpdatedAt = t
	}
	return &f, nil
}
