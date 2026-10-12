package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type CodexModelPreset struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// MarshalJSON preserves an explicit empty map (disable all prefixes), while
// omitting a nil map (inherit legacy aliases).
func (settings CodexSettings) MarshalJSON() ([]byte, error) {
	type plain CodexSettings
	var presets *map[string]CodexModelPreset
	if settings.ModelPresets != nil {
		presets = &settings.ModelPresets
	}
	return json.Marshal(struct {
		plain
		ModelPresets *map[string]CodexModelPreset `json:"modelPresets,omitempty"`
	}{plain: plain(settings), ModelPresets: presets})
}

var codexModelPresetAlias = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func ValidateCodexModelPresets(presets map[string]CodexModelPreset) error {
	for alias, preset := range presets {
		if !codexModelPresetAlias.MatchString(alias) {
			return fmt.Errorf("invalid codex model preset alias %q: use a lowercase ASCII letter followed by up to 31 letters, digits, _ or -", alias)
		}
		if strings.TrimSpace(preset.Model) == "" {
			return fmt.Errorf("codex model preset %q requires a model", alias)
		}
		if err := ValidateCodexRemoteDefault(preset.Model, preset.ReasoningEffort); err != nil {
			return fmt.Errorf("codex model preset %q: %w", alias, err)
		}
	}
	return nil
}

func normalizeCodexModelPresets(presets map[string]CodexModelPreset) map[string]CodexModelPreset {
	if presets == nil {
		return nil
	}
	normalized := make(map[string]CodexModelPreset, len(presets))
	for alias, preset := range presets {
		normalized[alias] = CodexModelPreset{Model: strings.TrimSpace(preset.Model), ReasoningEffort: strings.TrimSpace(preset.ReasoningEffort)}
	}
	return normalized
}

// EffectiveCodexModelPresets resolves absent settings to the legacy aliases.
// A non-nil map replaces the entire catalog, including an empty map.
func EffectiveCodexModelPresets(presets map[string]CodexModelPreset) map[string]CodexModelPreset {
	if presets != nil {
		return normalizeCodexModelPresets(presets)
	}
	defaults := state.DefaultCodexModelPresets()
	out := make(map[string]CodexModelPreset, len(defaults))
	for alias, preset := range defaults {
		out[alias] = CodexModelPreset{Model: preset.Model, ReasoningEffort: preset.ReasoningEffort}
	}
	return out
}
