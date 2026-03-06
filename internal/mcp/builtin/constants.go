package builtin

// Built-in tool name constants.
// All code that references built-in tool names should use these constants rather than hardcoded strings.
const (
	// Vulnerability management tool
	ToolRecordVulnerability = "record_vulnerability"

	// Knowledge base tools
	ToolListKnowledgeRiskTypes = "list_knowledge_risk_types"
	ToolSearchKnowledgeBase    = "search_knowledge_base"

	// Skills tools
	ToolListSkills = "list_skills"
	ToolReadSkill  = "read_skill"

	// Time awareness tool
	ToolGetCurrentTime = "get_current_time"

	// Persistent memory tools
	ToolStoreMemory        = "store_memory"
	ToolRetrieveMemory     = "retrieve_memory"
	ToolListMemories       = "list_memories"
	ToolDeleteMemory       = "delete_memory"
	ToolUpdateMemoryStatus = "update_memory_status"
)

// IsBuiltinTool reports whether the given tool name is a built-in tool.
func IsBuiltinTool(toolName string) bool {
	switch toolName {
	case ToolRecordVulnerability,
		ToolListKnowledgeRiskTypes,
		ToolSearchKnowledgeBase,
		ToolListSkills,
		ToolReadSkill,
		ToolGetCurrentTime,
		ToolStoreMemory,
		ToolRetrieveMemory,
		ToolListMemories,
		ToolDeleteMemory,
		ToolUpdateMemoryStatus:
		return true
	default:
		return false
	}
}

// GetAllBuiltinTools returns the list of all built-in tool names.
func GetAllBuiltinTools() []string {
	return []string{
		ToolRecordVulnerability,
		ToolListKnowledgeRiskTypes,
		ToolSearchKnowledgeBase,
		ToolListSkills,
		ToolReadSkill,
		ToolGetCurrentTime,
		ToolStoreMemory,
		ToolRetrieveMemory,
		ToolListMemories,
		ToolDeleteMemory,
		ToolUpdateMemoryStatus,
	}
}
