package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/database"
)

// BatchTask represents a batch task item
type BatchTask struct {
	ID             string     `json:"id"`
	Message        string     `json:"message"`
	ConversationID string     `json:"conversationId,omitempty"`
	Status         string     `json:"status"` // pending, running, completed, failed, cancelled
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	Error          string     `json:"error,omitempty"`
	Result         string     `json:"result,omitempty"`
}

// BatchTaskQueue represents a batch task queue
type BatchTaskQueue struct {
	ID           string       `json:"id"`
	Title        string       `json:"title,omitempty"`
	Role         string       `json:"role,omitempty"` // role name (empty string means default role)
	Tasks        []*BatchTask `json:"tasks"`
	Status       string       `json:"status"` // pending, running, paused, completed, cancelled
	CreatedAt    time.Time    `json:"createdAt"`
	StartedAt    *time.Time   `json:"startedAt,omitempty"`
	CompletedAt  *time.Time   `json:"completedAt,omitempty"`
	CurrentIndex int          `json:"currentIndex"`
	mu           sync.RWMutex
}

// BatchTaskManager manages batch task queues
type BatchTaskManager struct {
	db          *database.DB
	queues      map[string]*BatchTaskQueue
	taskCancels map[string]context.CancelFunc // stores the cancel function for each queue's current task
	mu          sync.RWMutex
}

// NewBatchTaskManager creates a new batch task manager
func NewBatchTaskManager() *BatchTaskManager {
	return &BatchTaskManager{
		queues:      make(map[string]*BatchTaskQueue),
		taskCancels: make(map[string]context.CancelFunc),
	}
}

// SetDB sets the database connection
func (m *BatchTaskManager) SetDB(db *database.DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db = db
}

// CreateBatchQueue creates a batch task queue
func (m *BatchTaskManager) CreateBatchQueue(title, role string, tasks []string) *BatchTaskQueue {
	m.mu.Lock()
	defer m.mu.Unlock()

	queueID := time.Now().Format("20060102150405") + "-" + generateShortID()
	queue := &BatchTaskQueue{
		ID:           queueID,
		Title:        title,
		Role:         role,
		Tasks:        make([]*BatchTask, 0, len(tasks)),
		Status:       "pending",
		CreatedAt:    time.Now(),
		CurrentIndex: 0,
	}

	// prepare task data for database save
	dbTasks := make([]map[string]interface{}, 0, len(tasks))

	for _, message := range tasks {
		if message == "" {
			continue // skip empty lines
		}
		taskID := generateShortID()
		task := &BatchTask{
			ID:      taskID,
			Message: message,
			Status:  "pending",
		}
		queue.Tasks = append(queue.Tasks, task)
		dbTasks = append(dbTasks, map[string]interface{}{
			"id":      taskID,
			"message": message,
		})
	}

	// save to database
	if m.db != nil {
		if err := m.db.CreateBatchQueue(queueID, title, role, dbTasks); err != nil {
			// if database save fails, log the error but continue (use memory cache)
			// logging can be added here
		}
	}

	m.queues[queueID] = queue
	return queue
}

// GetBatchQueue retrieves a batch task queue
func (m *BatchTaskManager) GetBatchQueue(queueID string) (*BatchTaskQueue, bool) {
	m.mu.RLock()
	queue, exists := m.queues[queueID]
	m.mu.RUnlock()

	if exists {
		return queue, true
	}

	// if not in memory, try loading from database
	if m.db != nil {
		if queue := m.loadQueueFromDB(queueID); queue != nil {
			m.mu.Lock()
			m.queues[queueID] = queue
			m.mu.Unlock()
			return queue, true
		}
	}

	return nil, false
}

// loadQueueFromDB loads a single queue from the database
func (m *BatchTaskManager) loadQueueFromDB(queueID string) *BatchTaskQueue {
	if m.db == nil {
		return nil
	}

	queueRow, err := m.db.GetBatchQueue(queueID)
	if err != nil || queueRow == nil {
		return nil
	}

	taskRows, err := m.db.GetBatchTasks(queueID)
	if err != nil {
		return nil
	}

	queue := &BatchTaskQueue{
		ID:           queueRow.ID,
		Status:       queueRow.Status,
		CreatedAt:    queueRow.CreatedAt,
		CurrentIndex: queueRow.CurrentIndex,
		Tasks:        make([]*BatchTask, 0, len(taskRows)),
	}

	if queueRow.Title.Valid {
		queue.Title = queueRow.Title.String
	}
	if queueRow.Role.Valid {
		queue.Role = queueRow.Role.String
	}
	if queueRow.StartedAt.Valid {
		queue.StartedAt = &queueRow.StartedAt.Time
	}
	if queueRow.CompletedAt.Valid {
		queue.CompletedAt = &queueRow.CompletedAt.Time
	}

	for _, taskRow := range taskRows {
		task := &BatchTask{
			ID:      taskRow.ID,
			Message: taskRow.Message,
			Status:  taskRow.Status,
		}
		if taskRow.ConversationID.Valid {
			task.ConversationID = taskRow.ConversationID.String
		}
		if taskRow.StartedAt.Valid {
			task.StartedAt = &taskRow.StartedAt.Time
		}
		if taskRow.CompletedAt.Valid {
			task.CompletedAt = &taskRow.CompletedAt.Time
		}
		if taskRow.Error.Valid {
			task.Error = taskRow.Error.String
		}
		if taskRow.Result.Valid {
			task.Result = taskRow.Result.String
		}
		queue.Tasks = append(queue.Tasks, task)
	}

	return queue
}

// GetAllQueues retrieves all queues
func (m *BatchTaskManager) GetAllQueues() []*BatchTaskQueue {
	m.mu.RLock()
	result := make([]*BatchTaskQueue, 0, len(m.queues))
	for _, queue := range m.queues {
		result = append(result, queue)
	}
	m.mu.RUnlock()

	// if database is available, ensure all queues in the database are loaded into memory
	if m.db != nil {
		dbQueues, err := m.db.GetAllBatchQueues()
		if err == nil {
			m.mu.Lock()
			for _, queueRow := range dbQueues {
				if _, exists := m.queues[queueRow.ID]; !exists {
					if queue := m.loadQueueFromDB(queueRow.ID); queue != nil {
						m.queues[queueRow.ID] = queue
						result = append(result, queue)
					}
				}
			}
			m.mu.Unlock()
		}
	}

	return result
}

// ListQueues lists queues (supports filtering and pagination)
func (m *BatchTaskManager) ListQueues(limit, offset int, status, keyword string) ([]*BatchTaskQueue, int, error) {
	var queues []*BatchTaskQueue
	var total int

	// if database is available, query from database
	if m.db != nil {
		// get total count
		count, err := m.db.CountBatchQueues(status, keyword)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count total queues: %w", err)
		}
		total = count

		// get queue list (IDs only)
		queueRows, err := m.db.ListBatchQueues(limit, offset, status, keyword)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to query queue list: %w", err)
		}

		// load complete queue info (from memory or database)
		m.mu.Lock()
		for _, queueRow := range queueRows {
			var queue *BatchTaskQueue
			// check memory first
			if cached, exists := m.queues[queueRow.ID]; exists {
				queue = cached
			} else {
				// load from database
				queue = m.loadQueueFromDB(queueRow.ID)
				if queue != nil {
					m.queues[queueRow.ID] = queue
				}
			}
			if queue != nil {
				queues = append(queues, queue)
			}
		}
		m.mu.Unlock()
	} else {
		// no database, filter and paginate from memory
		m.mu.RLock()
		allQueues := make([]*BatchTaskQueue, 0, len(m.queues))
		for _, queue := range m.queues {
			allQueues = append(allQueues, queue)
		}
		m.mu.RUnlock()

		// filter
		filtered := make([]*BatchTaskQueue, 0)
		for _, queue := range allQueues {
			// status filter
			if status != "" && status != "all" && queue.Status != status {
				continue
			}
			// keyword search (search queue ID and title)
			if keyword != "" {
				keywordLower := strings.ToLower(keyword)
				queueIDLower := strings.ToLower(queue.ID)
				queueTitleLower := strings.ToLower(queue.Title)
				if !strings.Contains(queueIDLower, keywordLower) && !strings.Contains(queueTitleLower, keywordLower) {
					// also search by creation time
					createdAtStr := queue.CreatedAt.Format("2006-01-02 15:04:05")
					if !strings.Contains(createdAtStr, keyword) {
						continue
					}
				}
			}
			filtered = append(filtered, queue)
		}

		// sort by creation time descending
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		})

		total = len(filtered)

		// paginate
		start := offset
		if start > len(filtered) {
			start = len(filtered)
		}
		end := start + limit
		if end > len(filtered) {
			end = len(filtered)
		}
		if start < len(filtered) {
			queues = filtered[start:end]
		}
	}

	return queues, total, nil
}

// LoadFromDB loads all queues from the database
func (m *BatchTaskManager) LoadFromDB() error {
	if m.db == nil {
		return nil
	}

	queueRows, err := m.db.GetAllBatchQueues()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, queueRow := range queueRows {
		if _, exists := m.queues[queueRow.ID]; exists {
			continue // already exists, skip
		}

		taskRows, err := m.db.GetBatchTasks(queueRow.ID)
		if err != nil {
			continue // skip queues that fail to load
		}

		queue := &BatchTaskQueue{
			ID:           queueRow.ID,
			Status:       queueRow.Status,
			CreatedAt:    queueRow.CreatedAt,
			CurrentIndex: queueRow.CurrentIndex,
			Tasks:        make([]*BatchTask, 0, len(taskRows)),
		}

		if queueRow.Title.Valid {
			queue.Title = queueRow.Title.String
		}
		if queueRow.Role.Valid {
			queue.Role = queueRow.Role.String
		}
		if queueRow.StartedAt.Valid {
			queue.StartedAt = &queueRow.StartedAt.Time
		}
		if queueRow.CompletedAt.Valid {
			queue.CompletedAt = &queueRow.CompletedAt.Time
		}

		for _, taskRow := range taskRows {
			task := &BatchTask{
				ID:      taskRow.ID,
				Message: taskRow.Message,
				Status:  taskRow.Status,
			}
			if taskRow.ConversationID.Valid {
				task.ConversationID = taskRow.ConversationID.String
			}
			if taskRow.StartedAt.Valid {
				task.StartedAt = &taskRow.StartedAt.Time
			}
			if taskRow.CompletedAt.Valid {
				task.CompletedAt = &taskRow.CompletedAt.Time
			}
			if taskRow.Error.Valid {
				task.Error = taskRow.Error.String
			}
			if taskRow.Result.Valid {
				task.Result = taskRow.Result.String
			}
			queue.Tasks = append(queue.Tasks, task)
		}

		m.queues[queueRow.ID] = queue
	}

	return nil
}

// UpdateTaskStatus updates task status
func (m *BatchTaskManager) UpdateTaskStatus(queueID, taskID, status string, result, errorMsg string) {
	m.UpdateTaskStatusWithConversationID(queueID, taskID, status, result, errorMsg, "")
}

// UpdateTaskStatusWithConversationID updates task status (including conversationId)
func (m *BatchTaskManager) UpdateTaskStatusWithConversationID(queueID, taskID, status string, result, errorMsg, conversationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return
	}

	for _, task := range queue.Tasks {
		if task.ID == taskID {
			task.Status = status
			if result != "" {
				task.Result = result
			}
			if errorMsg != "" {
				task.Error = errorMsg
			}
			if conversationID != "" {
				task.ConversationID = conversationID
			}
			now := time.Now()
			if status == "running" && task.StartedAt == nil {
				task.StartedAt = &now
			}
			if status == "completed" || status == "failed" || status == "cancelled" {
				task.CompletedAt = &now
			}
			break
		}
	}

	// sync to database
	if m.db != nil {
		if err := m.db.UpdateBatchTaskStatus(queueID, taskID, status, conversationID, result, errorMsg); err != nil {
			// log error but continue (use memory cache)
		}
	}
}

// UpdateQueueStatus updates queue status
func (m *BatchTaskManager) UpdateQueueStatus(queueID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return
	}

	queue.Status = status
	now := time.Now()
	if status == "running" && queue.StartedAt == nil {
		queue.StartedAt = &now
	}
	if status == "completed" || status == "cancelled" {
		queue.CompletedAt = &now
	}

	// sync to database
	if m.db != nil {
		if err := m.db.UpdateBatchQueueStatus(queueID, status); err != nil {
			// log error but continue (use memory cache)
		}
	}
}

// UpdateTaskMessage updates a task message (only for pending queues)
func (m *BatchTaskManager) UpdateTaskMessage(queueID, taskID, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return fmt.Errorf("queue not found")
	}

	// check queue status: only pending queues can have tasks edited
	if queue.Status != "pending" {
		return fmt.Errorf("only queues in pending status can have tasks edited")
	}

	// find and update the task
	for _, task := range queue.Tasks {
		if task.ID == taskID {
			// only pending tasks can be edited
			if task.Status != "pending" {
				return fmt.Errorf("only tasks in pending status can be edited")
			}
			task.Message = message

			// sync to database
			if m.db != nil {
				if err := m.db.UpdateBatchTaskMessage(queueID, taskID, message); err != nil {
					return fmt.Errorf("failed to update task message: %w", err)
				}
			}
			return nil
		}
	}

	return fmt.Errorf("task not found")
}

// AddTaskToQueue adds a task to the queue (only for pending queues)
func (m *BatchTaskManager) AddTaskToQueue(queueID, message string) (*BatchTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return nil, fmt.Errorf("queue not found")
	}

	// check queue status: only pending queues can have tasks added
	if queue.Status != "pending" {
		return nil, fmt.Errorf("only queues in pending status can have tasks added")
	}

	if message == "" {
		return nil, fmt.Errorf("task message cannot be empty")
	}

	// generate task ID
	taskID := generateShortID()
	task := &BatchTask{
		ID:      taskID,
		Message: message,
		Status:  "pending",
	}

	// add to memory queue
	queue.Tasks = append(queue.Tasks, task)

	// sync to database
	if m.db != nil {
		if err := m.db.AddBatchTask(queueID, taskID, message); err != nil {
			// if database save fails, remove from memory
			queue.Tasks = queue.Tasks[:len(queue.Tasks)-1]
			return nil, fmt.Errorf("failed to add task: %w", err)
		}
	}

	return task, nil
}

// DeleteTask deletes a task (only for pending queues)
func (m *BatchTaskManager) DeleteTask(queueID, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return fmt.Errorf("queue not found")
	}

	// check queue status: only pending queues can have tasks deleted
	if queue.Status != "pending" {
		return fmt.Errorf("only queues in pending status can have tasks deleted")
	}

	// find and delete the task
	taskIndex := -1
	for i, task := range queue.Tasks {
		if task.ID == taskID {
			// only pending tasks can be deleted
			if task.Status != "pending" {
				return fmt.Errorf("only tasks in pending status can be deleted")
			}
			taskIndex = i
			break
		}
	}

	if taskIndex == -1 {
		return fmt.Errorf("task not found")
	}

	// remove from memory queue
	queue.Tasks = append(queue.Tasks[:taskIndex], queue.Tasks[taskIndex+1:]...)

	// sync to database
	if m.db != nil {
		if err := m.db.DeleteBatchTask(queueID, taskID); err != nil {
			// if database deletion fails, restore the task in memory
			// for simplicity, we just log the error here
			return fmt.Errorf("failed to delete task: %w", err)
		}
	}

	return nil
}

// GetNextTask retrieves the next pending task
func (m *BatchTaskManager) GetNextTask(queueID string) (*BatchTask, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return nil, false
	}

	for i := queue.CurrentIndex; i < len(queue.Tasks); i++ {
		task := queue.Tasks[i]
		if task.Status == "pending" {
			queue.CurrentIndex = i
			return task, true
		}
	}

	return nil, false
}

// MoveToNextTask advances to the next task
func (m *BatchTaskManager) MoveToNextTask(queueID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue, exists := m.queues[queueID]
	if !exists {
		return
	}

	queue.CurrentIndex++

	// sync to database
	if m.db != nil {
		if err := m.db.UpdateBatchQueueCurrentIndex(queueID, queue.CurrentIndex); err != nil {
			// log error but continue (use memory cache)
		}
	}
}

// SetTaskCancel sets the cancel function for the current task
func (m *BatchTaskManager) SetTaskCancel(queueID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel != nil {
		m.taskCancels[queueID] = cancel
	} else {
		delete(m.taskCancels, queueID)
	}
}

// PauseQueue pauses a queue
func (m *BatchTaskManager) PauseQueue(queueID string) bool {
	m.mu.Lock()

	queue, exists := m.queues[queueID]
	if !exists {
		m.mu.Unlock()
		return false
	}

	if queue.Status != "running" {
		m.mu.Unlock()
		return false
	}

	queue.Status = "paused"

	// cancel the currently running task (by cancelling context)
	if cancel, exists := m.taskCancels[queueID]; exists {
		cancel()
		delete(m.taskCancels, queueID)
	}

	m.mu.Unlock()

	// sync queue status to database
	if m.db != nil {
		if err := m.db.UpdateBatchQueueStatus(queueID, "paused"); err != nil {
			// log error but continue (use memory cache)
		}
	}

	return true
}

// CancelQueue cancels a queue (kept for backward compatibility, PauseQueue is recommended)
func (m *BatchTaskManager) CancelQueue(queueID string) bool {
	m.mu.Lock()

	queue, exists := m.queues[queueID]
	if !exists {
		m.mu.Unlock()
		return false
	}

	if queue.Status == "completed" || queue.Status == "cancelled" {
		m.mu.Unlock()
		return false
	}

	queue.Status = "cancelled"
	now := time.Now()
	queue.CompletedAt = &now

	// cancel all pending tasks
	for _, task := range queue.Tasks {
		if task.Status == "pending" {
			task.Status = "cancelled"
			task.CompletedAt = &now
			// sync to database
			if m.db != nil {
				m.db.UpdateBatchTaskStatus(queueID, task.ID, "cancelled", "", "", "")
			}
		}
	}

	// cancel the currently running task
	if cancel, exists := m.taskCancels[queueID]; exists {
		cancel()
		delete(m.taskCancels, queueID)
	}

	m.mu.Unlock()

	// sync queue status to database
	if m.db != nil {
		if err := m.db.UpdateBatchQueueStatus(queueID, "cancelled"); err != nil {
			// log error but continue (use memory cache)
		}
	}

	return true
}

// DeleteQueue deletes a queue
func (m *BatchTaskManager) DeleteQueue(queueID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, exists := m.queues[queueID]
	if !exists {
		return false
	}

	// clean up cancel function
	delete(m.taskCancels, queueID)

	// delete from database
	if m.db != nil {
		if err := m.db.DeleteBatchQueue(queueID); err != nil {
			// log error but continue (use memory cache)
		}
	}

	delete(m.queues, queueID)
	return true
}

// generateShortID generates a short ID
func generateShortID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return time.Now().Format("150405") + "-" + hex.EncodeToString(b)
}
