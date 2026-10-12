package orchestrator

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestReasoningChangeServerConfirmationReachesNoticeAndFinalTag(t *testing.T) {
	for _, observedEffort := range []string{"max", "xhigh"} {
		t.Run(observedEffort, func(t *testing.T) {
			now := time.Date(2026, 10, 9, 8, 20, 25, 0, time.UTC)
			svc := newReplyAutoSteerServiceFixture(&now)
			sent := svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1", MessageID: "msg-reasoning", Text: "continue"})
			command := findPromptSendCommand(sent)
			if command == nil {
				t.Fatal("missing prompt command")
			}
			command.Overrides.ReasoningEffort = "max"
			tr := codex.NewTranslator("inst-1")
			observe := func(frame string) codex.Result {
				t.Helper()
				result, err := tr.ObserveServer([]byte(frame))
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			observe(`{"method":"thread/started","params":{"thread":{"id":"thread-1","modelProvider":"openai","model":"gpt-6.1-sol","config":{"model_reasoning_effort":"high"}}}}`)
			if _, err := tr.TranslateCommand(*command); err != nil {
				t.Fatal(err)
			}
			// Native Codex confirms settings before ACK and turn/started; the request is not evidence.
			settings := fmt.Sprintf(`{"method":"thread/settings/updated","params":{"threadId":"thread-1","threadSettings":{"model":"gpt-6.1-sol","modelProvider":"openai","effort":%q,"collaborationMode":{"mode":"default","settings":{"model":"gpt-6.1-sol","reasoning_effort":%q,"developer_instructions":null}}}}}`, observedEffort, observedEffort)
			observe(settings)
			result := observe(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-reasoning","status":"inProgress","items":[]}}}`)
			if len(result.Events) != 1 {
				t.Fatalf("started events: %#v", result.Events)
			}
			started := svc.ApplyAgentEvent("inst-1", result.Events[0])
			want := "本次回复模型：gpt-6.1-sol / " + observedEffort + "。"
			assertModelNotice(t, started, want)
			projector := feishu.NewProjector()
			for _, event := range started {
				if event.TimelineText != nil && event.TimelineText.Type == control.TimelineTextTurnModelStarted {
					ops := projector.ProjectEvent("chat-1", event)
					if len(ops) != 1 || ops[0].Text != want || ops[0].ReplyToMessageID != "msg-reasoning" {
						t.Fatalf("wrong start projection: %#v", ops)
					}
				}
			}
			// Later thread updates must not rewrite this turn's frozen display.
			for _, event := range observe(`{"method":"thread/settings/updated","params":{"threadId":"thread-1","threadSettings":{"model":"gpt-6.1-sol","effort":"low"}}}`).Events {
				svc.ApplyAgentEvent("inst-1", event)
			}
			assertModelNotice(t, svc.ApplyAgentEvent("inst-1", result.Events[0]), "")
			now = now.Add(time.Second)
			for _, event := range completeRemoteTurnWithFinalText(t, svc, "turn-reasoning", "completed", "", "done", nil) {
				if event.FinalTurnSummary == nil {
					continue
				}
				if event.FinalTurnSummary.Model != "gpt-6.1-sol" || event.FinalTurnSummary.ReasoningEffort != observedEffort {
					t.Fatalf("wrong final tuple: %#v", event.FinalTurnSummary)
				}
				payload := fmt.Sprint(projector.ProjectEvent("chat-1", event))
				if !strings.Contains(payload, observedEffort+"</text_tag>") {
					t.Fatalf("missing final effort tag: %s", payload)
				}
				return
			}
			t.Fatal("missing final summary")
		})
	}
}
