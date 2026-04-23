package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newMinimalConfigHandler builds a ConfigHandler wired to a temp yaml file
// so UpdateConfig can call saveConfig without touching the real fs.
func newMinimalConfigHandler(t *testing.T, cfg *config.Config, sink *fakeSink) *ConfigHandler {
	t.Helper()
	// write a minimal valid yaml config file
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("debug:\n  enabled: false\n  retain_days: 7\n"), 0600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return &ConfigHandler{
		configPath: cfgPath,
		config:     cfg,
		debugSink:  sink,
		logger:     zap.NewNop(),
	}
}

func TestSettingsSave_TogglesDebugSink(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Start with debug disabled both in config and on sink.
	cfg := &config.Config{}
	cfg.Debug.Enabled = false
	fake := &fakeSink{enabled: false}

	h := newMinimalConfigHandler(t, cfg, fake)

	// POST body: enable debug.
	body := UpdateConfigRequest{
		Debug: &config.DebugConfig{Enabled: true, RetainDays: 14},
	}
	raw, _ := json.Marshal(body)

	r := gin.New()
	r.PUT("/api/config", h.UpdateConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// The in-memory config must be updated.
	if !cfg.Debug.Enabled {
		t.Error("h.config.Debug.Enabled: want true, got false")
	}
	if cfg.Debug.RetainDays != 14 {
		t.Errorf("h.config.Debug.RetainDays: want 14, got %d", cfg.Debug.RetainDays)
	}

	// The sink's atomic bool must be flipped.
	if !fake.Enabled() {
		t.Error("fakeSink.Enabled(): want true after toggle, got false")
	}
}

func TestSettingsSave_TogglesDebugSink_Disable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Start with debug enabled.
	cfg := &config.Config{}
	cfg.Debug.Enabled = true
	fake := &fakeSink{enabled: true}

	h := newMinimalConfigHandler(t, cfg, fake)

	body := UpdateConfigRequest{
		Debug: &config.DebugConfig{Enabled: false, RetainDays: 7},
	}
	raw, _ := json.Marshal(body)

	r := gin.New()
	r.PUT("/api/config", h.UpdateConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	if cfg.Debug.Enabled {
		t.Error("h.config.Debug.Enabled: want false after disable, got true")
	}

	if fake.Enabled() {
		t.Error("fakeSink.Enabled(): want false after disable toggle, got true")
	}
}

func TestSettingsSave_NoDebugField_SinkUnchanged(t *testing.T) {
	// When the PUT body omits the debug field, the sink must not change.
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Debug.Enabled = true
	fake := &fakeSink{enabled: true}

	h := newMinimalConfigHandler(t, cfg, fake)

	// body with no Debug field
	body := UpdateConfigRequest{}
	raw, _ := json.Marshal(body)

	r := gin.New()
	r.PUT("/api/config", h.UpdateConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// Sink must remain unchanged.
	if !fake.Enabled() {
		t.Error("fakeSink.Enabled(): want true (unchanged) when no debug field in request")
	}
}
