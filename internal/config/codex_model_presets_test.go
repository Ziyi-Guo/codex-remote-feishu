package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexModelPresetsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presets map[string]CodexModelPreset
	}{
		{"missing", nil}, {"disabled", map[string]CodexModelPreset{}},
		{"configured", map[string]CodexModelPreset{"sol": {Model: " gpt-6.1-sol ", ReasoningEffort: " high "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultAppConfig()
			cfg.Codex.ModelPresets = tc.presets
			path := filepath.Join(t.TempDir(), "config.json")
			if err := WriteAppConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			var root map[string]json.RawMessage
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(root["codex"], &payload); err != nil {
				t.Fatal(err)
			}
			_, present := payload["modelPresets"]
			if present != (tc.presets != nil) {
				t.Fatalf("modelPresets presence=%v raw=%s", present, raw)
			}
			loaded, err := LoadAppConfigAtPath(path)
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.Config.Codex.ModelPresets
			if (got == nil) != (tc.presets == nil) {
				t.Fatalf("nil distinction lost: %#v", got)
			}
			effective := EffectiveCodexModelPresets(got)
			switch tc.name {
			case "missing":
				if effective["sol"].Model != "gpt-6-sol" {
					t.Fatalf("legacy presets=%#v", effective)
				}
			case "disabled":
				if len(effective) != 0 {
					t.Fatalf("disabled presets=%#v", effective)
				}
			case "configured":
				if len(effective) != 1 || effective["sol"].Model != "gpt-6.1-sol" || effective["sol"].ReasoningEffort != "high" {
					t.Fatalf("configured presets=%#v", effective)
				}
			}
		})
	}
}

func TestValidateCodexModelPresets(t *testing.T) {
	for _, alias := range []string{"", "Sol", "1sol", "sol space", "sol.dot", strings.Repeat("a", 33), "模型"} {
		if err := ValidateCodexModelPresets(map[string]CodexModelPreset{alias: {Model: "gpt-test", ReasoningEffort: "high"}}); err == nil {
			t.Errorf("accepted alias %q", alias)
		}
	}
	for _, preset := range []CodexModelPreset{{}, {Model: "gpt-test"}, {ReasoningEffort: "high"}, {Model: "gpt-test", ReasoningEffort: "ultra"}, {Model: "gpt-\ntest", ReasoningEffort: "high"}} {
		if err := ValidateCodexModelPresets(map[string]CodexModelPreset{"sol": preset}); err == nil {
			t.Errorf("accepted preset %#v", preset)
		}
	}
	if err := ValidateCodexModelPresets(map[string]CodexModelPreset{"my_sol-2": {Model: "gpt-test", ReasoningEffort: "max"}}); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCodexModelPresetRejectedOnReadAndWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultAppConfig()
	cfg.Codex.ModelPresets = map[string]CodexModelPreset{"Bad": {Model: "gpt-test", ReasoningEffort: "high"}}
	if err := WriteAppConfig(path, cfg); err == nil {
		t.Fatal("write accepted invalid alias")
	}
	if err := os.WriteFile(path, []byte(`{"codex":{"modelPresets":{"Bad":{"model":"gpt-test","reasoningEffort":"high"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAppConfigAtPath(path); err == nil {
		t.Fatal("load accepted invalid alias")
	}
}
