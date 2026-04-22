package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DebugConfig(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	yaml := `
openai:
  api_key: test
  base_url: https://example.invalid
  model: test
debug:
  enabled: true
  retain_days: 7
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Debug.Enabled {
		t.Fatalf("Debug.Enabled: want true, got false")
	}
	if cfg.Debug.RetainDays != 7 {
		t.Fatalf("Debug.RetainDays: want 7, got %d", cfg.Debug.RetainDays)
	}
}
