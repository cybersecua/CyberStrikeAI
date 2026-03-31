package handler

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrTaskCancelled 用户取消任务的错误
var ErrTaskCancelled = errors.New("agent task cancelled by user")

// ErrTaskAlreadyRunning 会话已有任务正在执行
var ErrTaskAlreadyRunning = errors.New("agent task already running for conversation")

// AgentTask 描述正在运行的Agent任务
type AgentTask struct {
	ConversationID string    `json:"conversationId"`
	Message        string    `json:"message,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
	Status         string    `json:"status"`
	CancellingAt   time.Time `json:"-"` // 进入 cancelling 状态的时间，用于清理长时间卡住的任务

	cancel func(error)
}

// CompletedTask 已完成的任务（用于历史记录）
type CompletedTask struct {
	ConversationID string    `json:"conversationId"`
	Message        string    `json:"message,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
	CompletedAt    time.Time `json:"completedAt"`
	Status         string    `json:"status"`
}

// AgentTaskManager 管理正在运行的Agent任务
type AgentTaskManager struct {
	mu             sync.RWMutex
	tasks          map[string]*AgentTask
	completedTasks []*CompletedTask // 最近完成的任务历史
	maxHistorySize int              // 最大历史记录数
	historyRetention time.Duration  // 历史记录保留时间
}

const (
	// cancellingStuckThreshold 处于「取消中」超过此时长则强制从运行列表移除。正常取消会在当前步骤内返回，
	// 超过则视为卡住，尽快释放会话。常见做法多为 30–60s 内释放。
	cancellingStuckThreshold = 45 * time.Second
	// cancellingStuckThresholdLegacy 未记录 CancellingAt 时用 StartedAt 判断的兜底时长
	cancellingStuckThresholdLegacy = 2 * time.Minute
	cleanupInterval                = 15 * time.Second // 与上面阈值配合，最长约 60s 内移除
)

// NewAgentTaskManager 创建任务管理器
func NewAgentTaskManager() *AgentTaskManager {
	m := &AgentTaskManager{
		tasks:            make(map[string]*AgentTask),
		completedTasks:   make([]*CompletedTask, 0),
		maxHistorySize:   50,              // 最多保留50条历史记录
		historyRetention: 24 * time.Hour,  // 保留24小时
	}
	go m.runStuckCancellingCleanup()
	return m
}

// runStuckCancellingCleanup 定期将长时间处于「取消中」的任务强制结束，避免卡住无法发新消息
func (m *AgentTaskManager) runStuckCancellingCleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		m.cleanupStuckCancelling()
	}
}

func (m *AgentTaskManager) cleanupStuckCancelling() {
	m.mu.Lock()
	var toFinish []string
	now := time.Now()
	for id, task := range m.tasks {
		if task.Status != "cancelling" {
			continue
		}
		var elapsed time.Duration
		if !task.CancellingAt.IsZero() {
			elapsed = now.Sub(task.CancellingAt)
			if elapsed < cancellingStuckThreshold {
				continue
			}
		} else {
			elapsed = now.Sub(task.StartedAt)
			if elapsed < cancellingStuckThresholdLegacy {
				continue
			}
		}
		toFinish = append(toFinish, id)
	}
	m.mu.Unlock()
	for _, id := range toFinish {
		m.FinishTask(id, "cancelled")
	}
}

// StartTask 注册并开始一个新的任务
func (m *AgentTaskManager) StartTask(conversationID, message string, cancel context.CancelCauseFunc) (*AgentTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tasks[conversationID]; exists {
		return nil, ErrTaskAlreadyRunning
	}

	task := &AgentTask{
		ConversationID: conversationID,
		Message:        message,
		StartedAt:      time.Now(),
		Status:         "running",
		cancel: func(err error) {
			if cancel != nil {
				cancel(err)
			}
		},
	}

	m.tasks[conversationID] = task
	return task, nil
}

// CancelTask 取消指定会话的任务。若任务已在取消中，仍返回 (true, nil) 以便接口幂等、前端不报错。
func (m *AgentTaskManager) CancelTask(conversationID string, cause error) (bool, error) {
	m.mu.Lock()
	task, exists := m.tasks[conversationID]
	if !exists {
		m.mu.Unlock()
		return false, nil
	}

	// 如果已经处于取消流程，视为成功（幂等），避免前端重复点击报「未找到任务」
	if task.Status == "cancelling" {
		m.mu.Unlock()
		return true, nil
	}

	task.Status = "cancelling"
	task.CancellingAt = time.Now()
	cancel := task.cancel
	m.mu.Unlock()

	if cause == nil {
		cause = ErrTaskCancelled
	}
	if cancel != nil {
		cancel(cause)
	}
	return true, nil
}

// UpdateTaskStatus 更新任务状态但不删除任务（用于在发送事件前更新状态）
func (m *AgentTaskManager) UpdateTaskStatus(conversationID string, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[conversationID]
	if !exists {
		return
	}

	if status != "" {
		task.Status = status
	}
}

// FinishTask 完成任务并从管理器中移除
func (m *AgentTaskManager) FinishTask(conversationID string, finalStatus string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[conversationID]
	if !exists {
		return
	}

	if finalStatus != "" {
		task.Status = finalStatus
	}

	// 保存到历史记录
	completedTask := &CompletedTask{
		ConversationID: task.ConversationID,
		Message:        task.Message,
		StartedAt:       task.StartedAt,
		CompletedAt:     time.Now(),
		Status:          finalStatus,
	}
	
	// 添加到历史记录
	m.completedTasks = append(m.completedTasks, completedTask)
	
	// 清理过期和过多的历史记录
	m.cleanupHistory()

	// 从运行任务中移除
	delete(m.tasks, conversationID)
}

// cleanupHistory 清理过期的历史记录
func (m *AgentTaskManager) cleanupHistory() {
	now := time.Now()
	cutoffTime := now.Add(-m.historyRetention)
	
	// 过滤掉过期的记录
	validTasks := make([]*CompletedTask, 0, len(m.completedTasks))
	for _, task := range m.completedTasks {
		if task.CompletedAt.After(cutoffTime) {
			validTasks = append(validTasks, task)
		}
	}
	
	// 如果仍然超过最大数量，只保留最新的
	if len(validTasks) > m.maxHistorySize {
		// 按完成时间排序，保留最新的
		// 由于是追加的，最新的在最后，所以直接取最后N个
		start := len(validTasks) - m.maxHistorySize
		validTasks = validTasks[start:]
	}
	
	m.completedTasks = validTasks
}

// GetActiveTasks 返回所有正在运行的任务
func (m *AgentTaskManager) GetActiveTasks() []*AgentTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*AgentTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		result = append(result, &AgentTask{
			ConversationID: task.ConversationID,
			Message:        task.Message,
			StartedAt:      task.StartedAt,
			Status:         task.Status,
		})
	}
	return result
}

// GetCompletedTasks 返回最近完成的任务历史
func (m *AgentTaskManager) GetCompletedTasks() []*CompletedTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// 清理过期记录（只读锁，不影响其他操作）
	// 注意：这里不能直接调用cleanupHistory，因为需要写锁
	// 所以返回时过滤过期记录
	now := time.Now()
	cutoffTime := now.Add(-m.historyRetention)
	
	result := make([]*CompletedTask, 0, len(m.completedTasks))
	for _, task := range m.completedTasks {
		if task.CompletedAt.After(cutoffTime) {
			result = append(result, task)
		}
	}
	
	// 按完成时间倒序排序（最新的在前）
	// 由于是追加的，最新的在最后，需要反转
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	
	// 限制返回数量
	if len(result) > m.maxHistorySize {
		result = result[:m.maxHistorySize]
	}
	
	return result
}
