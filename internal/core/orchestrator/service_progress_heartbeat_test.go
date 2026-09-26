package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func heartbeatNotice(events []eventcontract.Event) *eventcontract.Event {
	for i := range events {
		if events[i].Notice != nil && events[i].Notice.Code == "turn_progress_heartbeat" {
			return &events[i]
		}
	}
	return nil
}

func TestRunningRemoteTurnHeartbeatUsesDeliveredProgressAndStopsAtCompletion(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	surface := setupQueuedStartNoticeSurface(t, svc)
	svc.ApplySurfaceAction(control.Action{
		Kind: control.ActionTextMessage, SurfaceSessionID: surface.SurfaceSessionID,
		ChatID: surface.ChatID, ActorUserID: surface.ActorUserID,
		MessageID: "msg-1", Text: "处理长任务",
	})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind: agentproto.EventTurnStarted, ThreadID: "thread-1", TurnID: "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown},
	})
	if got := heartbeatNotice(svc.Tick(now.Add(9 * time.Minute))); got != nil {
		t.Fatalf("heartbeat before 10 minutes: %#v", got)
	}
	first := heartbeatNotice(svc.Tick(now.Add(10 * time.Minute)))
	if first == nil || first.SourceMessageID != "msg-1" {
		t.Fatalf("expected heartbeat on original reply lane, got %#v", first)
	}
	if got := heartbeatNotice(svc.Tick(now.Add(10*time.Minute + time.Second))); got != nil {
		t.Fatalf("heartbeat duplicated on next tick: %#v", got)
	}

	delivered := now.Add(10 * time.Minute)
	svc.RecordRemoteTurnVisibleProgress("surface-1", "turn-1", delivered)
	if got := heartbeatNotice(svc.Tick(delivered.Add(9 * time.Minute))); got != nil {
		t.Fatalf("heartbeat ignored delivered update: %#v", got)
	}
	if got := heartbeatNotice(svc.Tick(delivered.Add(10 * time.Minute))); got == nil {
		t.Fatal("expected second heartbeat after another 10 minutes")
	}

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind: agentproto.EventTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1",
		Status: "completed", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown},
	})
	if got := heartbeatNotice(svc.Tick(delivered.Add(30 * time.Minute))); got != nil {
		t.Fatalf("heartbeat after turn completed: %#v", got)
	}
}
