package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MonitorStorage interface for monitor data storage
type MonitorStorage interface {
	SaveToolExecution(exec *ToolExecution) error
	LoadToolExecutions() ([]*ToolExecution, error)
	GetToolExecution(id string) (*ToolExecution, error)
	SaveToolStats(toolName string, stats *ToolStats) error
	LoadToolStats() (map[string]*ToolStats, error)
	UpdateToolStats(toolName string, totalCalls, successCalls, failedCalls int, lastCallTime *time.Time) error
}

// Server MCP server
type Server struct {
	tools                 map[string]ToolHandler
	toolDefs              map[string]Tool // tool definitions
	executions            map[string]*ToolExecution
	stats                 map[string]*ToolStats
	prompts               map[string]*Prompt   // prompt templates
	resources             map[string]*Resource // resources
	storage               MonitorStorage       // optional persistent storage
	mu                    sync.RWMutex
	logger                *zap.Logger
	maxExecutionsInMemory int // max execution records in memory
	sseClients            map[string]*sseClient
}

type sseClient struct {
	id   string
	send chan []byte
}

// ToolHandler tool handler function
type ToolHandler func(ctx context.Context, args map[string]interface{}) (*ToolResult, error)

// NewServer creates a new MCP server
func NewServer(logger *zap.Logger) *Server {
	return NewServerWithStorage(logger, nil)
}

// NewServerWithStorage creates a new MCP server with persistent storage
func NewServerWithStorage(logger *zap.Logger, storage MonitorStorage) *Server {
	s := &Server{
		tools:                 make(map[string]ToolHandler),
		toolDefs:              make(map[string]Tool),
		executions:            make(map[string]*ToolExecution),
		stats:                 make(map[string]*ToolStats),
		prompts:               make(map[string]*Prompt),
		resources:             make(map[string]*Resource),
		storage:               storage,
		logger:                logger,
		maxExecutionsInMemory: 1000, // default: keep at most 1000 execution records in memory
		sseClients:            make(map[string]*sseClient),
	}

	// initialize default prompts (tool resources are created automatically
	// inside RegisterTool, so no separate resource-init step is needed)
	s.initDefaultPrompts()

	return s
}

// RegisterTool registers a tool
func (s *Server) RegisterTool(tool Tool, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = handler
	s.toolDefs[tool.Name] = tool

	// automatically create resource documentation for the tool
	resourceURI := fmt.Sprintf("tool://%s", tool.Name)
	s.resources[resourceURI] = &Resource{
		URI:         resourceURI,
		Name:        fmt.Sprintf("%s tool documentation", tool.Name),
		Description: tool.Description,
		MimeType:    "text/plain",
	}
}

// UnregisterTool removes a single tool by name (used for plugin hot-unload).
func (s *Server) UnregisterTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tools, name)
	delete(s.toolDefs, name)
	delete(s.resources, fmt.Sprintf("tool://%s", name))
}

// ClearTools clears all tools (used to reload configuration)
func (s *Server) ClearTools() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// clear tools and tool definitions
	s.tools = make(map[string]ToolHandler)
	s.toolDefs = make(map[string]Tool)

	// clear tool-related resources (keep other resources)
	newResources := make(map[string]*Resource)
	for uri, resource := range s.resources {
		// keep non-tool resources
		if !strings.HasPrefix(uri, "tool://") {
			newResources[uri] = resource
		}
	}
	s.resources = newResources
}

// HandleHTTP handles HTTP requests
func (s *Server) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.handleSSE(w, r)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// official MCP SSE spec: a POST with sessionid routes the message to that SSE session; response is returned via the SSE stream
	if sessionID := r.URL.Query().Get("sessionid"); sessionID != "" {
		s.serveSSESessionMessage(w, r, sessionID)
		return
	}

	// simple POST: request body is JSON-RPC, response is returned in the body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.sendError(w, nil, -32700, "Parse error", err.Error())
		return
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		s.sendError(w, nil, -32700, "Parse error", err.Error())
		return
	}

	response := s.handleMessage(&msg)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Warn("Failed to write MCP response", zap.Error(err))
	}
}

// serveSSESessionMessage handles a POST directed to an SSE session: reads the JSON-RPC request, processes it, and pushes the response via that session's SSE stream
func (s *Server) serveSSESessionMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.mu.RLock()
	client, exists := s.sseClients[sessionID]
	s.mu.RUnlock()
	if !exists || client == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "failed to parse body", http.StatusBadRequest)
		return
	}

	response := s.handleMessage(&msg)
	if response == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	select {
	case client.send <- respBytes:
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "session send buffer full", http.StatusServiceUnavailable)
	}
}

// handleSSE handles SSE connections, compatible with the official MCP 2024-11-05 SSE spec:
// 1. The first event must be event: endpoint, with data being the URL for the client to POST messages to (including sessionid)
// 2. Subsequent events are event: message, with data being JSON-RPC responses
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sessionID := uuid.New().String()
	client := &sseClient{
		id:   sessionID,
		send: make(chan []byte, 32),
	}

	s.addSSEClient(client)
	defer s.removeSSEClient(client.id)

	// official spec: first event is endpoint, data is the message endpoint URL (client will POST requests to this URL)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.URL.Scheme != "" {
		scheme = r.URL.Scheme
	}
	endpointURL := fmt.Sprintf("%s://%s%s?sessionid=%s", scheme, r.Host, r.URL.Path, sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-client.send:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// addSSEClient registers an SSE client
func (s *Server) addSSEClient(client *sseClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sseClients[client.id] = client
}

// removeSSEClient removes an SSE client
func (s *Server) removeSSEClient(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if client, exists := s.sseClients[id]; exists {
		close(client.send)
		delete(s.sseClients, id)
	}
}

// handleMessage handles an MCP message
func (s *Server) handleMessage(msg *Message) *Message {
	// check if this is a notification - notifications have no id field and don't need a response
	isNotification := msg.ID.Value() == nil || msg.ID.String() == ""

	// if not a notification and ID is empty, generate a new UUID
	if !isNotification && msg.ID.String() == "" {
		msg.ID = MessageID{value: uuid.New().String()}
	}

	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "tools/list":
		return s.handleListTools(msg)
	case "tools/call":
		return s.handleCallTool(msg)
	case "prompts/list":
		return s.handleListPrompts(msg)
	case "prompts/get":
		return s.handleGetPrompt(msg)
	case "resources/list":
		return s.handleListResources(msg)
	case "resources/read":
		return s.handleReadResource(msg)
	case "sampling/request":
		return s.handleSamplingRequest(msg)
	case "notifications/initialized":
		// notification type, no response needed
		s.logger.Debug("received initialized notification")
		return nil
	case "":
		// empty method name, may be a notification, don't return error
		if isNotification {
			s.logger.Debug("received notification with no method name")
			return nil
		}
		fallthrough
	default:
		// if it is a notification, don't return an error response
		if isNotification {
			s.logger.Debug("received unknown notification", zap.String("method", msg.Method))
			return nil
		}
		// for requests, return method not found error
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32601, Message: "Method not found"},
		}
	}
}

// marshalResult marshals v to JSON for a response. On the rare chance of failure,
// it logs the error and returns a JSON-RPC internal error response.
func (s *Server) marshalResult(msg *Message, v interface{}) *Message {
	result, err := json.Marshal(v)
	if err != nil {
		s.logger.Error("json.Marshal failed for MCP response", zap.Error(err))
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeResponse,
			Version: "2.0",
			Error:   &Error{Code: -32603, Message: "Internal error: failed to marshal response"},
		}
	}
	return &Message{
		ID:      msg.ID,
		Type:    MessageTypeResponse,
		Version: "2.0",
		Result:  result,
	}
}

// handleInitialize handles the initialize request
func (s *Server) handleInitialize(msg *Message) *Message {
	var req InitializeRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	response := InitializeResponse{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools: map[string]interface{}{
				"listChanged": true,
			},
			Prompts: map[string]interface{}{
				"listChanged": true,
			},
			Resources: map[string]interface{}{
				"subscribe":   true,
				"listChanged": true,
			},
			Sampling: map[string]interface{}{},
		},
		ServerInfo: ServerInfo{
			Name:    "CyberStrikeAI",
			Version: "1.0.0",
		},
	}

	return s.marshalResult(msg, response)
}

// handleListTools handles the list tools request
func (s *Server) handleListTools(msg *Message) *Message {
	s.mu.RLock()
	tools := make([]Tool, 0, len(s.toolDefs))
	for _, tool := range s.toolDefs {
		tools = append(tools, tool)
	}
	s.mu.RUnlock()
	s.logger.Debug("tools/list request", zap.Int("tool_count", len(tools)))

	response := ListToolsResponse{Tools: tools}
	return s.marshalResult(msg, response)
}

// handleCallTool handles a tool call request
func (s *Server) handleCallTool(msg *Message) *Message {
	var req CallToolRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	executionID := uuid.New().String()
	execution := &ToolExecution{
		ID:        executionID,
		ToolName:  req.Name,
		Arguments: req.Arguments,
		Status:    "running",
		StartTime: time.Now(),
	}

	s.mu.Lock()
	s.executions[executionID] = execution
	// if execution records in memory exceed the limit, clean up the oldest records
	s.cleanupOldExecutions()
	s.mu.Unlock()

	if s.storage != nil {
		if err := s.storage.SaveToolExecution(execution); err != nil {
			s.logger.Warn("failed to save execution record to database", zap.Error(err))
		}
	}

	s.mu.RLock()
	handler, exists := s.tools[req.Name]
	s.mu.RUnlock()

	if !exists {
		execution.Status = "failed"
		execution.Error = "Tool not found"
		now := time.Now()
		execution.EndTime = &now
		execution.Duration = now.Sub(execution.StartTime)

		if s.storage != nil {
			if err := s.storage.SaveToolExecution(execution); err != nil {
				s.logger.Warn("failed to save execution record to database", zap.Error(err))
			}
			s.mu.Lock()
			delete(s.executions, executionID)
			s.mu.Unlock()
		}

		s.updateStats(req.Name, true)

		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32601, Message: "Tool not found"},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s.logger.Info("starting tool execution",
		zap.String("toolName", req.Name),
		zap.Any("arguments", req.Arguments),
	)

	result, err := handler(ctx, req.Arguments)
	now := time.Now()
	var failed bool
	var finalResult *ToolResult

	s.mu.Lock()
	execution.EndTime = &now
	execution.Duration = now.Sub(execution.StartTime)

	if err != nil {
		execution.Status = "failed"
		execution.Error = err.Error()
		failed = true
	} else if result != nil && result.IsError {
		execution.Status = "failed"
		if len(result.Content) > 0 {
			execution.Error = result.Content[0].Text
		} else {
			execution.Error = "tool execution returned an error result"
		}
		execution.Result = result
		failed = true
	} else {
		execution.Status = "completed"
		if result == nil {
			result = &ToolResult{
				Content: []Content{
					{Type: "text", Text: "tool execution complete, but no result returned"},
				},
			}
		}
		execution.Result = result
		failed = false
	}

	finalResult = execution.Result
	s.mu.Unlock()

	if s.storage != nil {
		if err := s.storage.SaveToolExecution(execution); err != nil {
			s.logger.Warn("failed to save execution record to database", zap.Error(err))
		}
	}

	s.updateStats(req.Name, failed)

	if s.storage != nil {
		s.mu.Lock()
		delete(s.executions, executionID)
		s.mu.Unlock()
	}

	if err != nil {
		s.logger.Error("tool execution failed",
			zap.String("toolName", req.Name),
			zap.Error(err),
		)

		return s.marshalResult(msg, CallToolResponse{
			Content: []Content{
				{Type: "text", Text: fmt.Sprintf("tool execution failed: %v", err)},
			},
			IsError: true,
		})
	}

	if finalResult != nil && finalResult.IsError {
		s.logger.Warn("tool execution returned an error result",
			zap.String("toolName", req.Name),
		)

		return s.marshalResult(msg, CallToolResponse{
			Content: finalResult.Content,
			IsError: true,
		})
	}

	if finalResult == nil {
		finalResult = &ToolResult{
			Content: []Content{
				{Type: "text", Text: "tool execution complete, but no result returned"},
			},
		}
	}

	s.logger.Info("tool execution complete",
		zap.String("toolName", req.Name),
		zap.Bool("isError", finalResult.IsError),
	)

	return s.marshalResult(msg, CallToolResponse{
		Content: finalResult.Content,
		IsError: false,
	})
}

// updateStats updates statistics
func (s *Server) updateStats(toolName string, failed bool) {
	now := time.Now()
	if s.storage != nil {
		totalCalls := 1
		successCalls := 0
		failedCalls := 0
		if failed {
			failedCalls = 1
		} else {
			successCalls = 1
		}
		if err := s.storage.UpdateToolStats(toolName, totalCalls, successCalls, failedCalls, &now); err != nil {
			s.logger.Warn("failed to save statistics to database", zap.Error(err))
		}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stats[toolName] == nil {
		s.stats[toolName] = &ToolStats{
			ToolName: toolName,
		}
	}

	stats := s.stats[toolName]
	stats.TotalCalls++
	stats.LastCallTime = &now

	if failed {
		stats.FailedCalls++
	} else {
		stats.SuccessCalls++
	}
}

// GetExecution returns an execution record (searches memory first, then database)
func (s *Server) GetExecution(id string) (*ToolExecution, bool) {
	s.mu.RLock()
	exec, exists := s.executions[id]
	s.mu.RUnlock()

	if exists {
		return exec, true
	}

	if s.storage != nil {
		exec, err := s.storage.GetToolExecution(id)
		if err == nil {
			return exec, true
		}
	}

	return nil, false
}

// loadHistoricalData loads historical data from the database
func (s *Server) loadHistoricalData() {
	if s.storage == nil {
		return
	}

	// load historical execution records (most recent 1000)
	executions, err := s.storage.LoadToolExecutions()
	if err != nil {
		s.logger.Warn("failed to load historical execution records", zap.Error(err))
	} else {
		s.mu.Lock()
		for _, exec := range executions {
			// only load up to maxExecutionsInMemory records to avoid excessive memory usage
			if len(s.executions) < s.maxExecutionsInMemory {
				s.executions[exec.ID] = exec
			} else {
				break
			}
		}
		s.mu.Unlock()
		s.logger.Info("loaded historical execution records", zap.Int("count", len(executions)))
	}

	// load historical statistics
	stats, err := s.storage.LoadToolStats()
	if err != nil {
		s.logger.Warn("failed to load historical statistics", zap.Error(err))
	} else {
		s.mu.Lock()
		for k, v := range stats {
			s.stats[k] = v
		}
		s.mu.Unlock()
		s.logger.Info("loaded historical statistics", zap.Int("count", len(stats)))
	}
}

// GetAllExecutions returns all execution records (merged from memory and database)
func (s *Server) GetAllExecutions() []*ToolExecution {
	if s.storage != nil {
		dbExecutions, err := s.storage.LoadToolExecutions()
		if err == nil {
			execMap := make(map[string]*ToolExecution)
			for _, exec := range dbExecutions {
				if _, exists := execMap[exec.ID]; !exists {
					execMap[exec.ID] = exec
				}
			}

			s.mu.RLock()
			for id, exec := range s.executions {
				if _, exists := execMap[id]; !exists {
					execMap[id] = exec
				}
			}
			s.mu.RUnlock()

			result := make([]*ToolExecution, 0, len(execMap))
			for _, exec := range execMap {
				result = append(result, exec)
			}
			return result
		} else {
			s.logger.Warn("failed to load execution records from database", zap.Error(err))
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	memExecutions := make([]*ToolExecution, 0, len(s.executions))
	for _, exec := range s.executions {
		memExecutions = append(memExecutions, exec)
	}
	return memExecutions
}

// GetStats returns statistics (merged from memory and database)
func (s *Server) GetStats() map[string]*ToolStats {
	if s.storage != nil {
		dbStats, err := s.storage.LoadToolStats()
		if err == nil {
			return dbStats
		}
		s.logger.Warn("failed to load statistics from database", zap.Error(err))
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	memStats := make(map[string]*ToolStats)
	for k, v := range s.stats {
		statCopy := *v
		memStats[k] = &statCopy
	}

	return memStats
}

// GetAllTools returns all registered tools (used by Agent to dynamically get the tool list)
func (s *Server) GetAllTools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]Tool, 0, len(s.toolDefs))
	for _, tool := range s.toolDefs {
		tools = append(tools, tool)
	}
	return tools
}

// CallTool directly calls a tool (for internal use)
func (s *Server) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*ToolResult, string, error) {
	s.mu.RLock()
	handler, exists := s.tools[toolName]
	s.mu.RUnlock()

	if !exists {
		return nil, "", fmt.Errorf("tool %s not found", toolName)
	}

	// create execution record
	executionID := uuid.New().String()
	execution := &ToolExecution{
		ID:        executionID,
		ToolName:  toolName,
		Arguments: args,
		Status:    "running",
		StartTime: time.Now(),
	}

	s.mu.Lock()
	s.executions[executionID] = execution
	// if execution records in memory exceed the limit, clean up the oldest records
	s.cleanupOldExecutions()
	s.mu.Unlock()

	if s.storage != nil {
		if err := s.storage.SaveToolExecution(execution); err != nil {
			s.logger.Warn("failed to save execution record to database", zap.Error(err))
		}
	}

	result, err := handler(ctx, args)

	s.mu.Lock()
	now := time.Now()
	execution.EndTime = &now
	execution.Duration = now.Sub(execution.StartTime)
	var failed bool
	var finalResult *ToolResult

	if err != nil {
		execution.Status = "failed"
		execution.Error = err.Error()
		failed = true
	} else if result != nil && result.IsError {
		execution.Status = "failed"
		if len(result.Content) > 0 {
			execution.Error = result.Content[0].Text
		} else {
			execution.Error = "tool execution returned an error result"
		}
		execution.Result = result
		failed = true
		finalResult = result
	} else {
		execution.Status = "completed"
		if result == nil {
			result = &ToolResult{
				Content: []Content{
					{Type: "text", Text: "tool execution complete, but no result returned"},
				},
			}
		}
		execution.Result = result
		finalResult = result
		failed = false
	}

	if finalResult == nil {
		finalResult = execution.Result
	}
	s.mu.Unlock()

	if s.storage != nil {
		if err := s.storage.SaveToolExecution(execution); err != nil {
			s.logger.Warn("failed to save execution record to database", zap.Error(err))
		}
	}

	s.updateStats(toolName, failed)

	if s.storage != nil {
		s.mu.Lock()
		delete(s.executions, executionID)
		s.mu.Unlock()
	}

	if err != nil {
		return nil, executionID, err
	}

	return finalResult, executionID, nil
}

// cleanupOldExecutions cleans up old execution records to prevent unbounded memory growth
func (s *Server) cleanupOldExecutions() {
	if len(s.executions) <= s.maxExecutionsInMemory {
		return
	}

	// sort by start time, find the oldest records
	type execWithTime struct {
		id        string
		startTime time.Time
	}
	execs := make([]execWithTime, 0, len(s.executions))
	for id, exec := range s.executions {
		execs = append(execs, execWithTime{
			id:        id,
			startTime: exec.StartTime,
		})
	}

	// use sort package for efficient sorting (oldest first)
	sort.Slice(execs, func(i, j int) bool {
		return execs[i].startTime.Before(execs[j].startTime)
	})

	// delete the oldest records, keeping maxExecutionsInMemory records
	toDelete := len(s.executions) - s.maxExecutionsInMemory
	for i := 0; i < toDelete; i++ {
		delete(s.executions, execs[i].id)
	}

	s.logger.Debug("cleaned up old execution records",
		zap.Int("before", len(execs)),
		zap.Int("after", len(s.executions)),
		zap.Int("deleted", toDelete),
	)
}

// initDefaultPrompts initializes default prompt templates
func (s *Server) initDefaultPrompts() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// network security testing prompt
	s.prompts["security_scan"] = &Prompt{
		Name:        "security_scan",
		Description: "generate a prompt for a network security scan task",
		Arguments: []PromptArgument{
			{Name: "target", Description: "scan target (IP address or domain name)", Required: true},
			{Name: "scan_type", Description: "scan type (port, vuln, web, etc.)", Required: false},
		},
	}

	// penetration testing prompt
	s.prompts["penetration_test"] = &Prompt{
		Name:        "penetration_test",
		Description: "generate a prompt for a penetration testing task",
		Arguments: []PromptArgument{
			{Name: "target", Description: "test target", Required: true},
			{Name: "scope", Description: "test scope", Required: false},
		},
	}
}

// handleListPrompts handles the list prompts request
func (s *Server) handleListPrompts(msg *Message) *Message {
	s.mu.RLock()
	prompts := make([]Prompt, 0, len(s.prompts))
	for _, prompt := range s.prompts {
		prompts = append(prompts, *prompt)
	}
	s.mu.RUnlock()

	response := ListPromptsResponse{
		Prompts: prompts,
	}
	return s.marshalResult(msg, response)
}

// handleGetPrompt handles the get prompt request
func (s *Server) handleGetPrompt(msg *Message) *Message {
	var req GetPromptRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	s.mu.RLock()
	prompt, exists := s.prompts[req.Name]
	s.mu.RUnlock()

	if !exists {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32601, Message: "Prompt not found"},
		}
	}

	// generate messages based on the prompt name
	messages := s.generatePromptMessages(prompt, req.Arguments)

	response := GetPromptResponse{
		Messages: messages,
	}
	return s.marshalResult(msg, response)
}

// generatePromptMessages generates prompt messages
func (s *Server) generatePromptMessages(prompt *Prompt, args map[string]interface{}) []PromptMessage {
	messages := []PromptMessage{}

	switch prompt.Name {
	case "security_scan":
		target, _ := args["target"].(string)
		scanType, _ := args["scan_type"].(string)
		if scanType == "" {
			scanType = "comprehensive"
		}

		content := fmt.Sprintf(`Please perform a %s security scan on target %s. Include:
1. Port scanning and service identification
2. Vulnerability detection
3. Web application security testing
4. Generate a detailed security report`, scanType, target)

		messages = append(messages, PromptMessage{
			Role:    "user",
			Content: content,
		})

	case "penetration_test":
		target, _ := args["target"].(string)
		scope, _ := args["scope"].(string)

		content := fmt.Sprintf(`Please perform a penetration test on target %s.`, target)
		if scope != "" {
			content += fmt.Sprintf(" Test scope: %s", scope)
		}
		content += "\nPlease conduct a comprehensive security test following OWASP Top 10."

		messages = append(messages, PromptMessage{
			Role:    "user",
			Content: content,
		})

	default:
		messages = append(messages, PromptMessage{
			Role:    "user",
			Content: "Please perform a security testing task",
		})
	}

	return messages
}

// handleListResources handles the list resources request
func (s *Server) handleListResources(msg *Message) *Message {
	s.mu.RLock()
	resources := make([]Resource, 0, len(s.resources))
	for _, resource := range s.resources {
		resources = append(resources, *resource)
	}
	s.mu.RUnlock()

	response := ListResourcesResponse{
		Resources: resources,
	}
	return s.marshalResult(msg, response)
}

// handleReadResource handles the read resource request
func (s *Server) handleReadResource(msg *Message) *Message {
	var req ReadResourceRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	s.mu.RLock()
	resource, exists := s.resources[req.URI]
	s.mu.RUnlock()

	if !exists {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32601, Message: "Resource not found"},
		}
	}

	// generate resource content
	content := s.generateResourceContent(resource)

	response := ReadResourceResponse{
		Contents: []ResourceContent{content},
	}
	return s.marshalResult(msg, response)
}

// generateResourceContent generates resource content
func (s *Server) generateResourceContent(resource *Resource) ResourceContent {
	content := ResourceContent{
		URI:      resource.URI,
		MimeType: resource.MimeType,
	}

	// if it is a tool resource, generate detailed documentation
	if strings.HasPrefix(resource.URI, "tool://") {
		toolName := strings.TrimPrefix(resource.URI, "tool://")
		content.Text = s.generateToolDocumentation(toolName, resource)
	} else {
		// other resources use the description or default content
		content.Text = resource.Description
	}

	return content
}

// generateToolDocumentation generates tool documentation.
// Note: hardcoded tool documentation has been removed; only tool definition information is used now.
func (s *Server) generateToolDocumentation(toolName string, resource *Resource) string {
	// get tool definition for more detailed information
	s.mu.RLock()
	tool, hasTool := s.toolDefs[toolName]
	s.mu.RUnlock()

	// use description information from the tool definition
	if hasTool {
		doc := fmt.Sprintf("%s\n\n", resource.Description)
		if tool.InputSchema != nil {
			if props, ok := tool.InputSchema["properties"].(map[string]interface{}); ok {
				doc += "Parameter description:\n"
				for paramName, paramInfo := range props {
					if paramMap, ok := paramInfo.(map[string]interface{}); ok {
						if desc, ok := paramMap["description"].(string); ok {
							doc += fmt.Sprintf("- %s: %s\n", paramName, desc)
						}
					}
				}
			}
		}
		return doc
	}
	return resource.Description
}

// handleSamplingRequest handles a sampling request
func (s *Server) handleSamplingRequest(msg *Message) *Message {
	var req SamplingRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return &Message{
			ID:      msg.ID,
			Type:    MessageTypeError,
			Version: "2.0",
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	// Note: sampling functionality typically requires connecting to an actual LLM service.
	// This returns a placeholder response; actual implementation requires integrating the LLM API.
	s.logger.Warn("Sampling request received but not fully implemented",
		zap.Any("request", req),
	)

	response := SamplingResponse{
		Content: []SamplingContent{
			{
				Type: "text",
				Text: "Sampling requires an LLM service to be configured. Please use the Agent Loop API for AI conversation.",
			},
		},
		StopReason: "length",
	}
	return s.marshalResult(msg, response)
}

// RegisterPrompt registers a prompt template
func (s *Server) RegisterPrompt(prompt *Prompt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[prompt.Name] = prompt
}

// RegisterResource registers a resource
func (s *Server) RegisterResource(resource *Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[resource.URI] = resource
}

// HandleStdio handles standard input/output (for stdio transport mode).
// MCP protocol uses newline-delimited JSON-RPC messages; in pipe mode, Flush must be called after each write, otherwise the client won't receive the response.
func (s *Server) HandleStdio() error {
	decoder := json.NewDecoder(os.Stdin)
	stdout := bufio.NewWriter(os.Stdout)
	encoder := json.NewEncoder(stdout)
	// note: no indentation is set; MCP protocol expects compact JSON format

	for {
		var msg Message
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			// log to stderr to avoid interfering with stdout JSON-RPC communication
			s.logger.Error("failed to read message", zap.Error(err))
			// send error response
			errorMsg := Message{
				ID:      msg.ID,
				Type:    MessageTypeError,
				Version: "2.0",
				Error:   &Error{Code: -32700, Message: "Parse error", Data: err.Error()},
			}
			if err := encoder.Encode(errorMsg); err != nil {
				return fmt.Errorf("failed to send error response: %w", err)
			}
			if err := stdout.Flush(); err != nil {
				return fmt.Errorf("failed to flush stdout: %w", err)
			}
			continue
		}

		// handle message
		response := s.handleMessage(&msg)

		// if it is a notification (response is nil), no response needs to be sent
		if response == nil {
			continue
		}

		// send response
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("failed to send response: %w", err)
		}
		if err := stdout.Flush(); err != nil {
			return fmt.Errorf("failed to flush stdout: %w", err)
		}
	}

	return nil
}

// sendError sends an error response
func (s *Server) sendError(w http.ResponseWriter, id interface{}, code int, message, data string) {
	var msgID MessageID
	if id != nil {
		msgID = MessageID{value: id}
	}
	response := Message{
		ID:      msgID,
		Type:    MessageTypeError,
		Version: "2.0",
		Error:   &Error{Code: code, Message: message, Data: data},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Warn("Failed to write MCP error response", zap.Error(err))
	}
}
