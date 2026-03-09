package claude

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
)

// CLIConfig holds the Claude CLI configuration (mirrors config.ClaudeCLIConfig
// without importing the config package to avoid circular deps).
type CLIConfig struct {
	Workdir      string
	MaxTurns     int
	AllowedTools []string
	MCPServerURL string   // CyberStrikeAI MCP server URL (e.g. http://127.0.0.1:8081/mcp)
	MCPToolNames []string // Tool names registered in CyberStrikeAI's MCP server
}

// StreamAdapter wraps a CLIClient and SessionStore to provide a handler-friendly
// interface for routing chat messages through the Claude CLI binary.
type StreamAdapter struct {
	client   Client
	sessions *SessionStore
	cfg      CLIConfig
	logger   *zap.Logger
}

// NewStreamAdapter creates a StreamAdapter with a default CLIClient.
func NewStreamAdapter(cfg CLIConfig, logger *zap.Logger) *StreamAdapter {
	cli := NewCLIClient(logger)
	cli.workdir = cfg.Workdir
	return &StreamAdapter{
		client:   cli,
		sessions: NewSessionStore(),
		cfg:      cfg,
		logger:   logger,
	}
}

// NewStreamAdapterWithClient creates a StreamAdapter with a custom Client (useful for testing).
func NewStreamAdapterWithClient(client Client, logger *zap.Logger) *StreamAdapter {
	return &StreamAdapter{
		client:   client,
		sessions: NewSessionStore(),
		logger:   logger,
	}
}

// UpdateConfig hot-reloads the Claude CLI configuration (e.g. after UI settings apply).
func (a *StreamAdapter) UpdateConfig(cfg CLIConfig) {
	a.cfg = cfg
	// Update workdir on the underlying CLI client if possible
	if cli, ok := a.client.(*CLIClient); ok {
		cli.workdir = cfg.Workdir
	}
}

// RunPrompt sends a prompt through the Claude CLI and emits progress/response
// events via the callback. It handles session resumption for multi-turn conversations.
//
// The callback signature matches the sendEvent pattern used in AgentHandler:
//
//	callback(eventType, message string, data interface{})
//
// Returns the result text, the Claude session ID (for storage), and any error.
func (a *StreamAdapter) RunPrompt(
	ctx context.Context,
	prompt string,
	systemPrompt string,
	conversationID string,
	callback func(eventType, message string, data interface{}),
) (resultText string, claudeSessionID string, err error) {

	// Build options from config — merge static AllowedTools with MCP tool names
	allowedTools := append([]string{}, a.cfg.AllowedTools...)
	for _, name := range a.cfg.MCPToolNames {
		allowedTools = append(allowedTools, "mcp__cyberstrike__"+name)
	}
	opts := &PromptOptions{
		SystemPrompt: systemPrompt,
		MaxTurns:     a.cfg.MaxTurns,
		AllowedTools: allowedTools,
	}

	// Build MCP config JSON so Claude CLI can access CyberStrikeAI's security tools
	if a.cfg.MCPServerURL != "" {
		mcpCfg := map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"cyberstrike": map[string]interface{}{
					"type": "http",
					"url":  a.cfg.MCPServerURL,
				},
			},
		}
		if mcpJSON, err := json.Marshal(mcpCfg); err == nil {
			opts.MCPConfig = string(mcpJSON)
			a.logger.Debug("Passing MCP config to Claude CLI", zap.String("mcpConfig", opts.MCPConfig))
		} else {
			a.logger.Warn("Failed to marshal MCP config", zap.Error(err))
		}
	}

	// Resume existing session if available
	if existingSession := a.sessions.Get(conversationID); existingSession != "" {
		opts.SessionID = existingSession
		a.logger.Info("Resuming Claude CLI session",
			zap.String("conversationId", conversationID),
			zap.String("claudeSessionId", existingSession),
		)
	}

	callback("progress", "Sending request to Claude CLI...", map[string]interface{}{
		"provider": "claude-cli",
	})

	// Execute the prompt
	result, err := a.client.SendPrompt(ctx, prompt, opts)
	if err != nil {
		return "", "", fmt.Errorf("claude CLI execution failed: %w", err)
	}

	// Check for error result
	if result.IsError {
		return "", "", fmt.Errorf("claude CLI returned error: %s", result.Result)
	}

	// Store session ID for future turns
	if result.SessionID != "" {
		a.sessions.Set(conversationID, result.SessionID)
		a.logger.Info("Stored Claude CLI session",
			zap.String("conversationId", conversationID),
			zap.String("claudeSessionId", result.SessionID),
		)
	}

	// Emit usage info as progress event
	callback("progress", fmt.Sprintf("Claude CLI completed (%d turns, $%.4f)", result.NumTurns, result.CostUSD), map[string]interface{}{
		"provider":      "claude-cli",
		"num_turns":     result.NumTurns,
		"cost_usd":      result.CostUSD,
		"input_tokens":  result.Usage.InputTokens,
		"output_tokens": result.Usage.OutputTokens,
		"session_id":    result.SessionID,
	})

	return result.Result, result.SessionID, nil
}
