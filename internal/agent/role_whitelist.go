package agent

// isToolAllowed gates MCP-tool execution against the role's tool
// whitelist. An empty roleTools slice means "no role restriction"
// (the default). A non-empty slice requires exact-match inclusion.
//
// Why this exists as a separate gate: getAvailableTools / ToolsForRole
// is the DISPLAY filter — it controls which tools the LLM is shown in
// its tool list. This function is the EXECUTION gate — it prevents a
// hallucinated or history-leaked tool name from reaching
// executeToolViaMCP when the role excludes it. The single-agent loop
// previously only had the display filter; audit finding 1 flagged
// the gap. Multi-agent has an equivalent method on *orchestratorState
// (internal/multiagent/orchestrator.go) — identical semantics,
// deliberately not shared across packages to avoid an import cycle.
func isToolAllowed(name string, roleTools []string) bool {
	if len(roleTools) == 0 {
		return true
	}
	for _, t := range roleTools {
		if t == name {
			return true
		}
	}
	return false
}
