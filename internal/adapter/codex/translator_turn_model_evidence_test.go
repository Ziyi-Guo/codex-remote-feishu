package codex

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestTurnModelEvidenceInvalidatesChangedRequest(t *testing.T) {
	for name, policy := range map[string]*agentproto.CodexResumePolicy{
		"native": nil,
		"policy": &agentproto.CodexResumePolicy{Mode: agentproto.CodexResumePreserveThreadSettings, ModelProviderID: "provider", ModelMode: agentproto.CodexThreadValueDefault, ReasoningMode: agentproto.CodexThreadValueDefault},
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, model, effort, fresh, wantModel, wantEffort string
			}{
				{name: "model and effort changed", model: "new-model", effort: "high"},
				{name: "model changed", model: "new-model"},
				{name: "effort changed", effort: "high", wantModel: "old-model"},
				{name: "unchanged explicit", model: "old-model", effort: "low", wantModel: "old-model", wantEffort: "low"},
				{name: "defaults preserve observations", wantModel: "old-model", wantEffort: "low"},
				{name: "fresh evidence", model: "new-model", effort: "high", fresh: `{"method":"thread/settings/updated","params":{"threadId":"thread-1","settings":{"model":"actual-model","reasoningEffort":"medium"}}}`, wantModel: "actual-model", wantEffort: "medium"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tr := NewTranslator("inst-1")
					if _, err := tr.ObserveServer([]byte(`{"method":"thread/started","params":{"thread":{"id":"thread-1","modelProvider":"provider","model":"old-model","config":{"model_reasoning_effort":"low"}}}}`)); err != nil {
						t.Fatal(err)
					}
					commands, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandPromptSend, Origin: agentproto.Origin{Surface: "surface-1"}, Target: agentproto.Target{ThreadID: "thread-1", CWD: "/tmp/project"}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "next"}}}, Overrides: agentproto.PromptOverrides{Model: tc.model, ReasoningEffort: tc.effort}, CodexResume: policy})
					if err != nil {
						t.Fatal(err)
					}
					params := payloadParams(t, decodeSinglePayload(t, commands), "turn/start")
					if tc.model != "" && params["model"] != tc.model {
						t.Fatalf("outgoing model=%v", params["model"])
					}
					if tc.effort != "" && params["effort"] != tc.effort {
						t.Fatalf("outgoing effort=%v", params["effort"])
					}
					if tc.fresh != "" {
						if _, err := tr.ObserveServer([]byte(tc.fresh)); err != nil {
							t.Fatal(err)
						}
					}
					started, err := tr.ObserveServer([]byte(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-new"}}}`))
					if err != nil {
						t.Fatal(err)
					}
					event := started.Events[0]
					if event.Model != tc.wantModel || event.ReasoningEffort != tc.wantEffort {
						t.Fatalf("event model=%q/%q, want %q/%q", event.Model, event.ReasoningEffort, tc.wantModel, tc.wantEffort)
					}
					effective := event.CodexEffectiveThread
					if policy == nil {
						if effective != nil || event.Problem != nil {
							t.Fatalf("native model evidence must not require a resume policy: %#v", event)
						}
						return
					}
					if effective == nil || effective.Model != tc.wantModel || effective.ReasoningEffort != tc.wantEffort {
						t.Fatalf("effective=%#v, want %q/%q", effective, tc.wantModel, tc.wantEffort)
					}
				})
			}
		})
	}
}

func TestTurnModelEvidenceAcceptsNativeThreadSettingsUpdate(t *testing.T) {
	for _, tc := range []struct{ name, settings string }{
		// Captured from Codex 0.160.1 before the turn/start response and turn/started.
		{"native capture", `{"model":"gpt-6.1-sol","modelProvider":"openai","effort":"max","collaborationMode":{"mode":"default","settings":{"model":"gpt-6.1-sol","reasoning_effort":"max","developer_instructions":null}}}`},
		{"effort field", `{"model":"gpt-6.1-sol","modelProvider":"openai","effort":"max"}`},
		{"collaboration settings", `{"modelProvider":"openai","collaborationMode":{"mode":"default","settings":{"model":"gpt-6.1-sol","reasoning_effort":"max"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTranslator("inst-1")
			tr.currentThreadID = "thread-1"
			tr.mergeObservedThread("thread-1", "openai", "gpt-6.1-sol", "high")
			_, err := tr.TranslateCommand(agentproto.Command{
				Kind:      agentproto.CommandPromptSend,
				Origin:    agentproto.Origin{Surface: "surface-1"},
				Target:    agentproto.Target{ThreadID: "thread-1"},
				Overrides: agentproto.PromptOverrides{ReasoningEffort: "max"},
				CodexResume: &agentproto.CodexResumePolicy{
					Mode: agentproto.CodexResumePreserveThreadSettings, ModelProviderID: "openai",
					ModelMode: agentproto.CodexThreadValueDefault, ReasoningMode: agentproto.CodexThreadValueDefault,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if observed := tr.observedThreads["thread-1"]; observed.ReasoningEffort != "" {
				t.Fatalf("requested effort must not become observed evidence: %#v", observed)
			}
			updated, err := tr.ObserveServer([]byte(`{"method":"thread/settings/updated","params":{"threadId":"thread-1","threadSettings":` + tc.settings + `}}`))
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.Events) != 1 || updated.Events[0].ThreadSettings == nil {
				t.Fatalf("native settings update was lost: %#v", updated)
			}
			if settings := updated.Events[0].ThreadSettings; settings.Model != "gpt-6.1-sol" || settings.ReasoningEffort != "max" {
				t.Fatalf("incorrect native settings evidence: %#v", settings)
			}
			started, err := tr.ObserveServer([]byte(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-max"}}}`))
			if err != nil {
				t.Fatal(err)
			}
			event := started.Events[0]
			if event.Model != "gpt-6.1-sol" || event.ReasoningEffort != "max" {
				t.Fatalf("turn lost native model evidence: %#v", event)
			}
			if effective := event.CodexEffectiveThread; effective == nil || effective.Model != "gpt-6.1-sol" || effective.ReasoningEffort != "max" {
				t.Fatalf("effective contract lost native model evidence: %#v", effective)
			}
		})
	}
}

func TestLocalRequestsInvalidateTurnModelEvidence(t *testing.T) {
	for _, method := range []string{"turn/start", "thread/resume"} {
		t.Run(method, func(t *testing.T) {
			tr := NewTranslator("inst-1")
			tr.mergeObservedThread("thread-1", "provider", "old-model", "low")
			if _, err := tr.ObserveClient([]byte(`{"id":"local","method":"` + method + `","params":{"threadId":"thread-1","model":"new-model","effort":"high"}}`)); err != nil {
				t.Fatal(err)
			}
			observed := tr.observedThreads["thread-1"]
			if observed.Model != "" || observed.ReasoningEffort != "" || observed.ModelProviderID != "provider" {
				t.Fatalf("stale observation retained: %#v", observed)
			}
		})
	}
}

func TestResumeRequestsInvalidateModelEvidence(t *testing.T) {
	for _, kind := range []agentproto.CommandKind{agentproto.CommandPromptSend, agentproto.CommandThreadCompactStart, "restart"} {
		t.Run(string(kind), func(t *testing.T) {
			tr := NewTranslator("inst-1")
			tr.mergeObservedThread("thread-1", "provider", "old-model", "low")
			policy := &agentproto.CodexResumePolicy{Mode: agentproto.CodexResumePreserveThreadSettings, ModelProviderID: "provider", ModelMode: agentproto.CodexThreadValueExplicit, Model: "new-model"}
			if kind == "restart" {
				tr.currentThreadID = "thread-1"
				tr.PrepareChildRestartRestorePolicy(policy)
				if _, _, _, err := tr.BuildChildRestartRestoreFrame("restart"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := tr.TranslateCommand(agentproto.Command{Kind: kind, Target: agentproto.Target{ThreadID: "thread-1"}, CodexResume: policy}); err != nil {
					t.Fatal(err)
				}
			}
			observed := tr.observedThreads["thread-1"]
			if observed.Model != "" || observed.ReasoningEffort != "" {
				t.Fatalf("resume retained pre-change evidence: %#v", observed)
			}
		})
	}
}
