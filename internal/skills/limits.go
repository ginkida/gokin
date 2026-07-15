package skills

const (
	// MaxRenderedSkillBytes caps the complete tool-result payload that may bypass
	// generic output compaction. SkillTool rejects a wrapper that exceeds this
	// bound (for example because SKILL.md nearly fills its own cap and lives at an
	// unusually long path). Keep this at or below the regular ToolResult.ToMap
	// content ceiling so an exempt result has no later truncation chokepoint
	// before its first model delivery.
	MaxRenderedSkillBytes = 30_000
)
