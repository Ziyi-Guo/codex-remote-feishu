package orchestrator

import "github.com/kxn/codex-remote-feishu/internal/core/state"

// SetCodexModelPresets publishes a validated preset snapshot for future inputs.
// Like other Service setters, the caller owns synchronization with dispatch.
// Nil preserves legacy presets; a non-nil empty map disables all aliases.
func (s *Service) SetCodexModelPresets(presets map[string]state.CodexPromptOverrideRecord) {
	if presets == nil {
		presets = state.DefaultCodexModelPresets()
	}
	s.codexModelPresets = cloneCodexModelPresets(presets)
}

func (s *Service) CodexModelPresets() map[string]state.CodexPromptOverrideRecord {
	if s.codexModelPresets == nil {
		return state.DefaultCodexModelPresets()
	}
	return cloneCodexModelPresets(s.codexModelPresets)
}

func (s *Service) CodexRemoteDefault() state.CodexPromptOverrideRecord {
	return s.codexRemoteDefault
}

func cloneCodexModelPresets(presets map[string]state.CodexPromptOverrideRecord) map[string]state.CodexPromptOverrideRecord {
	out := make(map[string]state.CodexPromptOverrideRecord, len(presets))
	for alias, preset := range presets {
		out[alias] = preset
	}
	return out
}
