package codex

import (
	"encoding/json"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestCurrentThreadRefreshesDeveloperInstructionsOnceAfterNativeAcceptance(t *testing.T) {
	tr := NewTranslator("inst")
	if _, err := tr.ObserveClient([]byte(`{"method":"thread/resume","params":{"threadId":"thread","cwd":"/tmp"}}`)); err != nil {
		t.Fatal(err)
	}
	c := agentproto.Command{CommandID: "command", Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: "thread"}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}}}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "short entry"}}
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	params := payloadParams(t, p, "thread/resume")
	if params["developerInstructions"] != "short entry" {
		t.Fatal(params)
	}
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}}})
	result, err := tr.ObserveServer(response)
	if err != nil || len(result.OutboundToCodex) != 1 {
		t.Fatalf("followup: %+v %v", result, err)
	}
	payloadParams(t, decodeSinglePayload(t, result.OutboundToCodex), "turn/start")
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "turn/start")
	c.CodexResume.DeveloperInstruction = "changed entry"
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "thread/resume")
}

func TestRejectedDeveloperInstructionRefreshDoesNotMarkApplied(t *testing.T) {
	tr := NewTranslator("inst")
	tr.currentThreadID = "thread"
	c := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: "thread"}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "short entry"}}
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	payloadParams(t, p, "thread/resume")
	response, _ := json.Marshal(map[string]any{"id": p["id"], "error": map[string]any{"message": "rejected"}})
	r, err := tr.ObserveServer(response)
	if err != nil || len(r.OutboundToCodex) != 0 {
		t.Fatalf("rejected refresh started a turn: %+v %v", r, err)
	}
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "thread/resume")
}

func TestNewThreadDeveloperEntryDoesNotRepeatInputAfterCompaction(t *testing.T) {
	tr := NewTranslator("inst")
	c := agentproto.Command{CommandID: "new", Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ExecutionMode: agentproto.PromptExecutionModeStartNew}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "original user input"}}}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "read the local guide when relevant"}}
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	params := payloadParams(t, p, "thread/start")
	if params["developerInstructions"] != c.CodexResume.DeveloperInstruction {
		t.Fatal(params)
	}
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}}})
	r, err := tr.ObserveServer(response)
	if err != nil || len(r.OutboundToCodex) != 1 {
		t.Fatalf("start response: %+v %v", r, err)
	}
	params = payloadParams(t, decodeSinglePayload(t, r.OutboundToCodex), "turn/start")
	input := params["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["text"] != "original user input" {
		t.Fatal(input)
	}
	if _, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"compact-turn","item":{"id":"compact-item","type":"contextCompaction"}}}`)); err != nil {
		t.Fatal(err)
	}
	c.Target = agentproto.Target{ThreadID: "thread"}
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	params = payloadParams(t, decodeSinglePayload(t, frames), "turn/start")
	if len(params["input"].([]any)) != 1 {
		t.Fatal(params)
	}
}

func TestDeveloperInstructionAcceptanceUsesDispatchedPolicySnapshot(t *testing.T) {
	tr := NewTranslator("inst")
	tr.currentThreadID = "thread"
	c := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: "thread"}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "sent instruction"}}
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	c.CodexResume.DeveloperInstruction = "later desired instruction"
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}}})
	if _, err := tr.ObserveServer(response); err != nil {
		t.Fatal(err)
	}
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "thread/resume")
}

func TestDeveloperEntryRefreshesAfterNativeResumeOrChildRestore(t *testing.T) {
	for _, action := range []string{"local_resume", "child_restore"} {
		t.Run(action, func(t *testing.T) {
			tr := NewTranslator("inst")
			tr.currentThreadID = "thread"
			c := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: "thread"}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "entry"}}
			frames, err := tr.TranslateCommand(c)
			if err != nil {
				t.Fatal(err)
			}
			p := decodeSinglePayload(t, frames)
			response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}}})
			if _, err := tr.ObserveServer(response); err != nil {
				t.Fatal(err)
			}
			if action == "local_resume" {
				if _, err := tr.ObserveClient([]byte(`{"method":"thread/resume","params":{"threadId":"thread","developerInstructions":"local rule"}}`)); err != nil {
					t.Fatal(err)
				}
			} else {
				tr.PrepareChildRestartRestorePolicy(&agentproto.CodexResumePolicy{ModelProviderID: "openai"})
				if _, _, ok, err := tr.BuildChildRestartRestoreFrame("restart"); err != nil || !ok {
					t.Fatalf("restore: %t %v", ok, err)
				}
			}
			frames, err = tr.TranslateCommand(c)
			if err != nil {
				t.Fatal(err)
			}
			payloadParams(t, decodeSinglePayload(t, frames), "thread/resume")
		})
	}
}

func TestDeveloperEntryNeverBecomesAnotherThreadsDefault(t *testing.T) {
	tr := NewTranslator("inst")
	c := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ExecutionMode: agentproto.PromptExecutionModeStartNew}, CodexResume: &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "private entry"}}
	if _, err := tr.TranslateCommand(c); err != nil {
		t.Fatal(err)
	}
	c.CodexResume = nil
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	params := payloadParams(t, decodeSinglePayload(t, frames), "thread/start")
	if params["developerInstructions"] != nil {
		t.Fatalf("private entry escaped thread: %+v", params)
	}
}
