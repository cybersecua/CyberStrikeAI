package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TestCheckClaudeCLI_NoBinary verifies that when the claude binary is not on
// PATH, the endpoint returns ok=false with a single "binary"/"fail" check and
// does not proceed to the auth or mcp_auth probes.
func TestCheckClaudeCLI_NoBinary(t *testing.T) {
	// Point PATH at an empty temp dir so exec.LookPath("claude") fails.
	t.Setenv("PATH", t.TempDir())

	h := &ConfigHandler{
		config: &config.Config{},
		logger: zap.NewNop(),
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/claude-cli/check", h.CheckClaudeCLI)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/claude-cli/check", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	var body ClaudeCLICheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response JSON: %v — body: %s", err, w.Body.String())
	}
	if body.OK {
		t.Fatalf("expected ok=false (binary missing), got ok=true; checks=%+v", body.Checks)
	}
	if len(body.Checks) != 1 {
		t.Fatalf("expected exactly 1 check (binary), got %d: %+v", len(body.Checks), body.Checks)
	}
	if body.Checks[0].Name != "binary" || body.Checks[0].Status != "fail" {
		t.Fatalf("expected binary/fail check, got %+v", body.Checks[0])
	}
}
