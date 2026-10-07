package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestConfiguredCodexModelPresetsFreezeConcreteSelection(t *testing.T) {
	now := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	svc := newReplyAutoSteerServiceFixture(&now)
	surface := svc.root.Surfaces["surface-1"]
	presets := map[string]state.CodexPromptOverrideRecord{"sol": {Model: "gpt-6.1-sol", ReasoningEffort: "medium"}, "fast": {Model: "gpt-6-luna", ReasoningEffort: "high"}}
	svc.SetCodexModelPresets(presets)
	// Mutating caller-owned config must not mutate the published snapshot.
	delete(presets, "sol")
	startReplyAutoSteerTurn(svc)
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: surface.SurfaceSessionID, MessageID: "configured", Text: "[SOL:high]检查", Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "[SOL:high]检查"}}})
	item := queueItemForSourceMessage(t, surface, "configured")
	if item.Status != state.QueueItemQueued {
		t.Fatalf("item must wait before config change: %s", item.Status)
	}
	if item.FrozenOverride.Model != "gpt-6.1-sol" || item.FrozenOverride.ReasoningEffort != "high" {
		t.Fatalf("frozen=%#v", item.FrozenOverride)
	}
	if item.CodexMessagePreset != "sol" || item.Inputs[0].Text != "检查" {
		t.Fatalf("queue=%#v", item)
	}
	if surface.CodexPromptOverride.Model != "gpt-6.1-sol" {
		t.Fatalf("topic=%#v", surface.CodexPromptOverride)
	}
	for _, next := range []map[string]state.CodexPromptOverrideRecord{{"sol": {Model: "gpt-new", ReasoningEffort: "max"}}, {}} {
		svc.SetCodexModelPresets(next)
		if _, problem, rejected := svc.codexMessagePresetDispatchGuard(surface, item); rejected {
			t.Fatalf("alias change rejected frozen item: %s", problem)
		}
		if item.FrozenOverride.Model != "gpt-6.1-sol" || surface.CodexPromptOverride.Model != "gpt-6.1-sol" {
			t.Fatal("config mutated queue/topic")
		}
	}
	events := completeRemoteTurnWithFinalText(t, svc, "turn-1", "completed", "", "", nil)
	command := findPromptSendCommand(events)
	if command == nil || command.Overrides.Model != "gpt-6.1-sol" || command.Overrides.ReasoningEffort != "high" {
		t.Fatalf("frozen model not dispatched after aliases disabled: %#v", command)
	}
}

func TestConfiguredCodexModelPresetsReplacementAndDisabled(t *testing.T) {
	svc := &Service{}
	for _, tc := range []struct {
		presets  map[string]state.CodexPromptOverrideRecord
		text     string
		explicit bool
		model    string
	}{
		{nil, "[sol]hi", true, "gpt-6-sol"},
		{map[string]state.CodexPromptOverrideRecord{}, "[sol]hi", false, ""},
		{map[string]state.CodexPromptOverrideRecord{"fast": {Model: "gpt-test", ReasoningEffort: "high"}}, "[sol]hi", false, ""},
		{map[string]state.CodexPromptOverrideRecord{"fast": {Model: "gpt-test", ReasoningEffort: "high"}}, "[FAST]hi", true, "gpt-test"},
	} {
		svc.SetCodexModelPresets(tc.presets)
		_, preset, explicit, err := parseCodexMessagePreset(tc.text, svc.codexModelPresets)
		if err != nil || explicit != tc.explicit || preset.Model != tc.model {
			t.Fatalf("parse %q: %#v %v %v", tc.text, preset, explicit, err)
		}
	}
	got := svc.CodexModelPresets()
	delete(got, "fast")
	if len(svc.CodexModelPresets()) != 1 {
		t.Fatal("getter leaked mutable map")
	}
}

func TestConfiguredCodexPresetDispatchStillChecksFrozenModel(t *testing.T) {
	now := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	svc := newReplyAutoSteerServiceFixture(&now)
	surface := svc.root.Surfaces["surface-1"]
	svc.SetCodexModelPresets(map[string]state.CodexPromptOverrideRecord{"fast": {Model: "gpt-test", ReasoningEffort: "high"}})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: surface.SurfaceSessionID, MessageID: "custom", Text: "[fast]检查"})
	item := queueItemForSourceMessage(t, surface, "custom")
	svc.SetCodexModelPresets(map[string]state.CodexPromptOverrideRecord{})
	inst := svc.root.Instances[surface.AttachedInstanceID]
	inst.ModelCatalog = &agentproto.ModelCatalogSnapshot{}
	if _, _, rejected := svc.codexMessagePresetDispatchGuard(surface, item); !rejected {
		t.Fatal("custom/removed alias bypassed frozen model validation")
	}
}
