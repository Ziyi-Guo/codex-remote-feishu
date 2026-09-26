package orchestrator

import (
	"fmt"
	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestTurnModelNoticeAndFinalSummary(t *testing.T) {
	for _, tc := range []struct{ name, input, model, effort, want string }{
		{"native", "hello", "gpt-6-astra", "medium", "本次回复模型：gpt-6-astra / medium。"},
		{"prefix", "[sol] hello", "gpt-6-sol", "high", "本次回复模型：gpt-6-sol / high。"},
		{"unknown", "hello", "", "", "本次回复模型：暂未获得运行确认。"},
		{"model only", "hello", "gpt-6-astra", "", "本次回复模型：gpt-6-astra。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
			svc := newReplyAutoSteerServiceFixture(&now)
			sent := svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1", MessageID: "msg-model", Text: tc.input})
			for _, e := range sent {
				if e.TimelineText != nil && strings.Contains(e.TimelineText.Text, "模型") {
					t.Fatalf("model reported before runtime confirmation: %s", e.TimelineText.Text)
				}
			}
			started := agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread-1", TurnID: "turn-model", Model: tc.model, ReasoningEffort: tc.effort}

			if tc.name == "native" {
				tr := codex.NewTranslator("inst-1")
				if _, err := tr.ObserveServer([]byte(`{"method":"thread/started","params":{"thread":{"id":"thread-1","modelProvider":"openai","model":"gpt-6-astra","config":{"model_reasoning_effort":"medium"}}}}`)); err != nil {
					t.Fatal(err)
				}
				result, err := tr.ObserveServer([]byte(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-model"}}}`))
				if err != nil {
					t.Fatal(err)
				}
				started = result.Events[0]
			}
			projector := feishu.NewProjector()
			events := svc.ApplyAgentEvent("inst-1", started)
			assertModelNotice(t, events, tc.want)
			for _, e := range events {
				if e.TimelineText != nil && strings.HasPrefix(e.TimelineText.Text, "本次回复模型：") {
					ops := projector.ProjectEvent("chat-1", e)
					if len(ops) != 1 || ops[0].Kind != feishu.OperationSendText || ops[0].Text != tc.want || ops[0].ReplyToMessageID != "msg-model" {
						t.Fatalf("bad model text projection: %#v", ops)
					}
				}
			}

			for _, e := range events {
				if e.TimelineText != nil && strings.HasPrefix(e.TimelineText.Text, "本次回复模型：") && e.TimelineText.ReplyToMessageID != "msg-model" {
					t.Fatal("wrong reply anchor")
				}
			}
			assertModelNotice(t, svc.ApplyAgentEvent("inst-1", started), "")
			now = now.Add(time.Second)
			finals := completeRemoteTurnWithFinalText(t, svc, "turn-model", "completed", "", "done", nil)
			found := false
			for _, e := range finals {
				if e.FinalTurnSummary != nil {
					found = true
					ops := projector.ProjectEvent("chat-1", e)
					if tc.model != "" && !strings.Contains(fmt.Sprint(ops), "**模型** <text_tag color='neutral'>"+tc.model+"</text_tag>") {
						t.Fatalf("missing final model tag: %#v", ops)
					}

					if e.FinalTurnSummary.Model != tc.model || e.FinalTurnSummary.ReasoningEffort != tc.effort {
						t.Fatalf("wrong footer: %#v", e.FinalTurnSummary)
					}
				}
			}
			if !found {
				t.Fatal("missing final summary")
			}
		})
	}
}

func assertModelNotice(t *testing.T, events []eventcontract.Event, want string) {
	t.Helper()
	count := 0
	for _, e := range events {
		if e.TimelineText != nil && strings.HasPrefix(e.TimelineText.Text, "本次回复模型：") {
			count++
			if e.TimelineText.Text != want {
				t.Fatalf("notice=%q, want %q", e.TimelineText.Text, want)
			}
		}
	}
	expected := 0
	if want != "" {
		expected = 1
	}
	if count != expected {
		t.Fatalf("model notices=%d, want %d", count, expected)
	}
}
