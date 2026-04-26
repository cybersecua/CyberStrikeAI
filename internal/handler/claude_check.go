package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"cyberstrike-ai/internal/claude"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ClaudeCLICheckResult is the per-probe result.
type ClaudeCLICheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "fail"
	Detail string `json:"detail"`
}

// ClaudeCLICheckResponse is the aggregated probe report.
type ClaudeCLICheckResponse struct {
	OK     bool                   `json:"ok"`
	Checks []ClaudeCLICheckResult `json:"checks"`
}

// CheckClaudeCLI handles POST /api/claude-cli/check. Runs three
// synchronous probes and returns aggregate readiness.
func (h *ConfigHandler) CheckClaudeCLI(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()

	checks := []ClaudeCLICheckResult{}
	allOK := true

	// Probe 1: binary on PATH.
	binaryPath, err := exec.LookPath("claude")
	if err != nil {
		checks = append(checks, ClaudeCLICheckResult{
			Name:   "binary",
			Status: "fail",
			Detail: "claude binary not found on PATH; install Claude CLI from https://docs.claude.com/en/docs/claude-code/quickstart",
		})
		c.JSON(http.StatusOK, ClaudeCLICheckResponse{OK: false, Checks: checks})
		return
	}
	checks = append(checks, ClaudeCLICheckResult{
		Name:   "binary",
		Status: "ok",
		Detail: "found at " + binaryPath,
	})

	// Probe 2: live ping (5s timeout).
	// Uses the same arg shape and JSON parsing that CLIClient.SendPrompt uses
	// so the probe validates the exact path the toggle will exercise.
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	pingResult := probeClaudePing(pingCtx, binaryPath, h.logger)
	checks = append(checks, pingResult)
	if pingResult.Status != "ok" {
		allOK = false
	}

	// Probe 3: MCP auth header configured (only matters if CLI will
	// call back to our MCP server, which it does once the toggle is on).
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()

	if cfg != nil && cfg.MCP.AuthHeader != "" && cfg.MCP.AuthHeaderValue != "" {
		checks = append(checks, ClaudeCLICheckResult{
			Name:   "mcp_auth",
			Status: "ok",
			Detail: "MCP auth header configured",
		})
	} else {
		checks = append(checks, ClaudeCLICheckResult{
			Name:   "mcp_auth",
			Status: "fail",
			Detail: "MCP auth header missing in config (mcp.auth_header + mcp.auth_header_value); the Claude CLI cannot authenticate to the framework's tool server",
		})
		allOK = false
	}

	c.JSON(http.StatusOK, ClaudeCLICheckResponse{OK: allOK, Checks: checks})
}

// probeClaudePing runs `claude -p - --output-format json` with stdin "ok"
// and a context-bound timeout.  Uses the same arg shape as CLIClient.SendPrompt
// so the probe validates exactly the path the toggle will exercise.
// Returns ok if the binary exits 0 AND the JSON result field is non-empty.
func probeClaudePing(ctx context.Context, binaryPath string, logger *zap.Logger) ClaudeCLICheckResult {
	cmd := exec.CommandContext(ctx, binaryPath, "-p", "-", "--output-format", "json")
	cmd.Stdin = strings.NewReader("ok")

	stdout, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return ClaudeCLICheckResult{
			Name:   "auth",
			Status: "fail",
			Detail: "claude ping timed out after 5s; CLI may be hanging on auth or network",
		}
	}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		snippet := strings.TrimSpace(string(stdout))
		if ok && len(exitErr.Stderr) > 0 {
			snippet = strings.TrimSpace(string(exitErr.Stderr))
		}
		if len(snippet) > 240 {
			snippet = snippet[:240] + "…"
		}
		return ClaudeCLICheckResult{
			Name:   "auth",
			Status: "fail",
			Detail: "claude ping failed: " + err.Error() + " — output: " + snippet,
		}
	}

	// Reuse the canonical Result type so parsing is identical to production.
	var resp claude.Result
	if jerr := json.Unmarshal(stdout, &resp); jerr != nil {
		return ClaudeCLICheckResult{
			Name:   "auth",
			Status: "fail",
			Detail: "claude returned non-JSON output: " + truncate(string(stdout), 240),
		}
	}
	if resp.IsError {
		return ClaudeCLICheckResult{
			Name:   "auth",
			Status: "fail",
			Detail: "claude reported error: " + truncate(resp.Result, 240),
		}
	}
	if strings.TrimSpace(resp.Result) == "" {
		return ClaudeCLICheckResult{
			Name:   "auth",
			Status: "fail",
			Detail: "claude returned empty result; check `claude config list` and `claude login`",
		}
	}
	if logger != nil {
		logger.Debug("claude-cli ping ok", zap.String("binary", binaryPath))
	}
	return ClaudeCLICheckResult{
		Name:   "auth",
		Status: "ok",
		Detail: "claude responded successfully to test prompt",
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
