package state

// DefaultCodexModelPresets preserves aliases used before presets were configurable.
// Return a fresh map so callers can publish independent configuration snapshots.
func DefaultCodexModelPresets() map[string]CodexPromptOverrideRecord {
	return map[string]CodexPromptOverrideRecord{
		"luna":  {Model: "gpt-6-luna", ReasoningEffort: "high"},
		"terra": {Model: "gpt-5.6-terra", ReasoningEffort: "high"},
		"sol":   {Model: "gpt-6-sol", ReasoningEffort: "high"},
		"astra": {Model: "gpt-6-astra", ReasoningEffort: "high"},
	}
}
