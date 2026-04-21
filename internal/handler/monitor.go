package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// MonitorHandler handles monitoring operations
type MonitorHandler struct {
	mcpServer      *mcp.Server
	externalMCPMgr *mcp.ExternalMCPManager
	executor       *security.Executor
	db             *database.DB
	logger         *zap.Logger
}

// NewMonitorHandler creates a new monitor handler
func NewMonitorHandler(mcpServer *mcp.Server, executor *security.Executor, db *database.DB, logger *zap.Logger) *MonitorHandler {
	return &MonitorHandler{
		mcpServer:      mcpServer,
		externalMCPMgr: nil, // will be set after creation
		executor:       executor,
		db:             db,
		logger:         logger,
	}
}

// SetExternalMCPManager sets the external MCP manager
func (h *MonitorHandler) SetExternalMCPManager(mgr *mcp.ExternalMCPManager) {
	h.externalMCPMgr = mgr
}

// MonitorResponse is the monitor response
type MonitorResponse struct {
	Executions []*mcp.ToolExecution      `json:"executions"`
	Stats      map[string]*mcp.ToolStats `json:"stats"`
	Timestamp  time.Time                 `json:"timestamp"`
	Total      int                       `json:"total,omitempty"`
	Page       int                       `json:"page,omitempty"`
	PageSize   int                       `json:"page_size,omitempty"`
	TotalPages int                       `json:"total_pages,omitempty"`
}

// Monitor retrieves monitoring information
func (h *MonitorHandler) Monitor(c *gin.Context) {
	// parse pagination parameters
	page := 1
	pageSize := 20
	if pageStr := c.Query("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if pageSizeStr := c.Query("page_size"); pageSizeStr != "" {
		if ps, err := strconv.Atoi(pageSizeStr); err == nil && ps > 0 && ps <= 100 {
			pageSize = ps
		}
	}

	// parse status filter parameter
	status := c.Query("status")
	// parse tool filter parameter
	toolName := c.Query("tool")

	executions, total := h.loadExecutionsWithPagination(page, pageSize, status, toolName)
	stats := h.loadStats()

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	c.JSON(http.StatusOK, MonitorResponse{
		Executions: executions,
		Stats:      stats,
		Timestamp:  time.Now(),
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	})
}

func (h *MonitorHandler) loadExecutions() []*mcp.ToolExecution {
	executions, _ := h.loadExecutionsWithPagination(1, 1000, "", "")
	return executions
}

func (h *MonitorHandler) loadExecutionsWithPagination(page, pageSize int, status, toolName string) ([]*mcp.ToolExecution, int) {
	if h.db == nil {
		allExecutions := h.mcpServer.GetAllExecutions()
		// if status or tool filter is specified, filter first
		if status != "" || toolName != "" {
			filtered := make([]*mcp.ToolExecution, 0)
			for _, exec := range allExecutions {
				matchStatus := status == "" || exec.Status == status
				// support partial match (fuzzy search)
				matchTool := toolName == "" || strings.Contains(strings.ToLower(exec.ToolName), strings.ToLower(toolName))
				if matchStatus && matchTool {
					filtered = append(filtered, exec)
				}
			}
			allExecutions = filtered
		}
		total := len(allExecutions)
		offset := (page - 1) * pageSize
		end := offset + pageSize
		if end > total {
			end = total
		}
		if offset >= total {
			return []*mcp.ToolExecution{}, total
		}
		return allExecutions[offset:end], total
	}

	offset := (page - 1) * pageSize
	executions, err := h.db.LoadToolExecutionsWithPagination(offset, pageSize, status, toolName)
	if err != nil {
		h.logger.Warn("failed to load execution records from database, falling back to memory data", zap.Error(err))
		allExecutions := h.mcpServer.GetAllExecutions()
		// if status or tool filter is specified, filter first
		if status != "" || toolName != "" {
			filtered := make([]*mcp.ToolExecution, 0)
			for _, exec := range allExecutions {
				matchStatus := status == "" || exec.Status == status
				// support partial match (fuzzy search)
				matchTool := toolName == "" || strings.Contains(strings.ToLower(exec.ToolName), strings.ToLower(toolName))
				if matchStatus && matchTool {
					filtered = append(filtered, exec)
				}
			}
			allExecutions = filtered
		}
		total := len(allExecutions)
		offset := (page - 1) * pageSize
		end := offset + pageSize
		if end > total {
			end = total
		}
		if offset >= total {
			return []*mcp.ToolExecution{}, total
		}
		return allExecutions[offset:end], total
	}

	// get total count (considering status and tool filters)
	total, err := h.db.CountToolExecutions(status, toolName)
	if err != nil {
		h.logger.Warn("failed to get total execution record count", zap.Error(err))
		// fallback: estimate from loaded records
		total = offset + len(executions)
		if len(executions) == pageSize {
			total = offset + len(executions) + 1
		}
	}

	return executions, total
}

func (h *MonitorHandler) loadStats() map[string]*mcp.ToolStats {
	// merge statistics from internal MCP server and external MCP manager
	stats := make(map[string]*mcp.ToolStats)

	// load statistics from internal MCP server
	if h.db == nil {
		internalStats := h.mcpServer.GetStats()
		for k, v := range internalStats {
			stats[k] = v
		}
	} else {
		dbStats, err := h.db.LoadToolStats()
		if err != nil {
			h.logger.Warn("failed to load statistics from database, falling back to memory data", zap.Error(err))
			internalStats := h.mcpServer.GetStats()
			for k, v := range internalStats {
				stats[k] = v
			}
		} else {
			for k, v := range dbStats {
				stats[k] = v
			}
		}
	}

	// merge statistics from external MCP manager
	if h.externalMCPMgr != nil {
		externalStats := h.externalMCPMgr.GetToolStats()
		for k, v := range externalStats {
			// if already exists, merge statistics
			if existing, exists := stats[k]; exists {
				existing.TotalCalls += v.TotalCalls
				existing.SuccessCalls += v.SuccessCalls
				existing.FailedCalls += v.FailedCalls
				// use the most recent call time
				if v.LastCallTime != nil && (existing.LastCallTime == nil || v.LastCallTime.After(*existing.LastCallTime)) {
					existing.LastCallTime = v.LastCallTime
				}
			} else {
				stats[k] = v
			}
		}
	}

	return stats
}

// GetExecution retrieves a specific execution record
func (h *MonitorHandler) GetExecution(c *gin.Context) {
	id := c.Param("id")

	// check internal MCP server first
	exec, exists := h.mcpServer.GetExecution(id)
	if exists {
		c.JSON(http.StatusOK, exec)
		return
	}

	// if not found, try the external MCP manager
	if h.externalMCPMgr != nil {
		exec, exists = h.externalMCPMgr.GetExecution(id)
		if exists {
			c.JSON(http.StatusOK, exec)
			return
		}
	}

	// if still not found, try the database (if using database storage)
	if h.db != nil {
		exec, err := h.db.GetToolExecution(id)
		if err == nil && exec != nil {
			c.JSON(http.StatusOK, exec)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "execution record not found"})
}

// GetStats retrieves statistics
func (h *MonitorHandler) GetStats(c *gin.Context) {
	stats := h.loadStats()
	c.JSON(http.StatusOK, stats)
}

// DeleteExecution deletes an execution record
func (h *MonitorHandler) DeleteExecution(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "execution record ID cannot be empty"})
		return
	}

	// if using database, get the execution record first, then delete and update stats
	if h.db != nil {
		// get execution record info first (for updating stats)
		exec, err := h.db.GetToolExecution(id)
		if err != nil {
			// if record not found, it may have already been deleted; return success
			h.logger.Warn("execution record not found, may have already been deleted", zap.String("executionId", id), zap.Error(err))
			c.JSON(http.StatusOK, gin.H{"message": "execution record not found or already deleted"})
			return
		}

		// delete execution record
		err = h.db.DeleteToolExecution(id)
		if err != nil {
			h.logger.Error("failed to delete execution record", zap.Error(err), zap.String("executionId", id))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete execution record: " + err.Error()})
			return
		}

		// update statistics (decrement corresponding counts)
		totalCalls := 1
		successCalls := 0
		failedCalls := 0
		if exec.Status == "failed" {
			failedCalls = 1
		} else if exec.Status == "completed" {
			successCalls = 1
		}

		if exec.ToolName != "" {
			if err := h.db.DecreaseToolStats(exec.ToolName, totalCalls, successCalls, failedCalls); err != nil {
				h.logger.Warn("failed to update statistics", zap.Error(err), zap.String("toolName", exec.ToolName))
				// do not return error since the record was already deleted successfully
			}
		}

		h.logger.Info("execution record deleted from database", zap.String("executionId", id), zap.String("toolName", exec.ToolName))
		c.JSON(http.StatusOK, gin.H{"message": "execution record deleted"})
		return
	}

	// if not using database, try deleting from memory (internal MCP server)
	// note: in-memory records may have already been cleaned up, so we just log here
	h.logger.Info("attempting to delete in-memory execution record", zap.String("executionId", id))
	c.JSON(http.StatusOK, gin.H{"message": "execution record deleted (if it existed)"})
}

// DeleteExecutions bulk deletes execution records
func (h *MonitorHandler) DeleteExecutions(c *gin.Context) {
	var request struct {
		IDs []string `json:"ids"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request parameters: " + err.Error()})
		return
	}

	if len(request.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "execution record ID list cannot be empty"})
		return
	}

	// if using database, get the execution records first, then delete and update stats
	if h.db != nil {
		// get execution record info first (for updating stats)
		executions, err := h.db.GetToolExecutionsByIds(request.IDs)
		if err != nil {
			h.logger.Error("failed to get execution records", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get execution records: " + err.Error()})
			return
		}

		// group stats by tool name to determine counts to decrement
		toolStats := make(map[string]struct {
			totalCalls   int
			successCalls int
			failedCalls  int
		})

		for _, exec := range executions {
			if exec.ToolName == "" {
				continue
			}

			stats := toolStats[exec.ToolName]
			stats.totalCalls++
			if exec.Status == "failed" {
				stats.failedCalls++
			} else if exec.Status == "completed" {
				stats.successCalls++
			}
			toolStats[exec.ToolName] = stats
		}

		// bulk delete execution records
		err = h.db.DeleteToolExecutions(request.IDs)
		if err != nil {
			h.logger.Error("failed to bulk delete execution records", zap.Error(err), zap.Int("count", len(request.IDs)))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to bulk delete execution records: " + err.Error()})
			return
		}

		// update statistics (decrement corresponding counts)
		for toolName, stats := range toolStats {
			if err := h.db.DecreaseToolStats(toolName, stats.totalCalls, stats.successCalls, stats.failedCalls); err != nil {
				h.logger.Warn("failed to update statistics", zap.Error(err), zap.String("toolName", toolName))
				// do not return error since the records were already deleted successfully
			}
		}

		h.logger.Info("bulk deleted execution records successfully", zap.Int("count", len(request.IDs)))
		c.JSON(http.StatusOK, gin.H{"message": "execution records deleted successfully", "deleted": len(executions)})
		return
	}

	// if not using database, try deleting from memory (internal MCP server)
	// note: in-memory records may have already been cleaned up, so we just log here
	h.logger.Info("attempting to bulk delete in-memory execution records", zap.Int("count", len(request.IDs)))
	c.JSON(http.StatusOK, gin.H{"message": "execution records deleted (if they existed)"})
}
