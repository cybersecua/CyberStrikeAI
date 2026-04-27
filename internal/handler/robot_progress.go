package handler

import "fmt"

// MajorEventStep translates the agent loop's progress event tuple
// (eventType, message, data) into a short single-line "step" string
// suitable for editing into a Telegram placeholder message. Returns
// the empty string for events that should be silently filtered (the
// telegram.go throttler treats empty as no-op).
//
// Rule set is fixed to per-major-event verbosity per the spec:
//   - iteration       → 🤔 Round N: thinking…
//   - tool_call       → 🔧 Round N: calling {tool}…
//   - tool_result     → ✅/❌ Round N: {tool} done|failed
//   - response_start  → ✍️ Drafting answer…
//   - everything else → "" (filtered)
//
// Throttling is the caller's responsibility — telegram.go already
// enforces a wall-clock minimum interval between editMessageText
// calls (telegramEditThrottle).
func MajorEventStep(eventType, message string, data map[string]interface{}) string {
	switch eventType {
	case "iteration":
		n := intFromData(data, "iteration", 0)
		return fmt.Sprintf("🤔 Round %d: thinking…", n)
	case "tool_call":
		tool := stringFromData(data, "toolName", "?")
		n := intFromData(data, "iteration", 0)
		return fmt.Sprintf("🔧 Round %d: calling %s…", n, tool)
	case "tool_result":
		tool := stringFromData(data, "toolName", "?")
		n := intFromData(data, "iteration", 0)
		success, _ := data["success"].(bool)
		if success {
			return fmt.Sprintf("✅ Round %d: %s done", n, tool)
		}
		return fmt.Sprintf("❌ Round %d: %s failed", n, tool)
	case "response_start":
		return "✍️ Drafting answer…"
	}
	return ""
}

func intFromData(data map[string]interface{}, key string, fallback int) int {
	if data == nil {
		return fallback
	}
	switch v := data[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return fallback
}

func stringFromData(data map[string]interface{}, key, fallback string) string {
	if data == nil {
		return fallback
	}
	if v, ok := data[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
