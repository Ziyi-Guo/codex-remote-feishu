package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
	"github.com/kxn/codex-remote-feishu/internal/core/renderer"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type heartbeatGateway struct{ messageIDAssigningGateway }

func (g *heartbeatGateway) Apply(ctx context.Context, operations []feishu.Operation) error {
	for i := range operations {
		if operations[i].Kind == feishu.OperationSendText {
			operations[i].MessageID = "om-progress-1"
		}
	}
	return g.messageIDAssigningGateway.Apply(ctx, operations)
}

func TestDeliveredAssistantProgressDefersAutomaticHeartbeat(t *testing.T) {
	base := time.Now().Add(-9 * time.Minute)
	workspaceDir := evalSymlinkForTest(t, t.TempDir())
	gateway := &heartbeatGateway{}
	app := New(":0", ":0", gateway, serverIdentityForTest())
	app.service = orchestrator.NewService(func() time.Time { return base }, orchestrator.Config{}, renderer.NewPlanner())
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID: "inst-1", Online: true, WorkspaceRoot: workspaceDir, WorkspaceKey: workspaceDir,
		ObservedFocusedThreadID: "thread-1",
		Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", CWD: workspaceDir, Loaded: true}},
	})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "inst-1"})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", ThreadID: "thread-1"})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", MessageID: "msg-1", Text: "处理长任务"})
	app.service.ApplyAgentEvent("inst-1", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread-1", TurnID: "turn-1", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown}})

	app.handleUIEvents(context.Background(), []eventcontract.Event{{
		Kind: eventcontract.KindBlockCommitted, SurfaceSessionID: "surface-1", SourceMessageID: "msg-1",
		Block: &render.Block{InstanceID: "inst-1", ThreadID: "thread-1", TurnID: "turn-1", Kind: render.BlockAssistantMarkdown, Text: "正在核对权限。"},
	}})
	if len(gateway.snapshotOperations()) == 0 {
		t.Fatal("expected assistant progress to reach gateway")
	}
	if got := heartbeatNoticeFromDaemon(app.service.Tick(base.Add(10 * time.Minute))); got != nil {
		t.Fatalf("delivered assistant progress did not defer heartbeat: %#v", got)
	}
	heartbeat := heartbeatNoticeFromDaemon(app.service.Tick(time.Now().Add(11 * time.Minute)))
	if heartbeat == nil {
		t.Fatal("expected automatic status after another 10 minutes")
	}
	app.handleUIEvents(context.Background(), []eventcontract.Event{*heartbeat})
	ops := gateway.snapshotOperations()
	last := ops[len(ops)-1]
	if last.Kind != feishu.OperationSendCard || last.ReplyToMessageID != "msg-1" {
		t.Fatalf("expected automatic status card on original reply lane, got %#v", last)
	}
}

func heartbeatNoticeFromDaemon(events []eventcontract.Event) *eventcontract.Event {
	for i := range events {
		if events[i].Notice != nil && events[i].Notice.Code == "turn_progress_heartbeat" {
			return &events[i]
		}
	}
	return nil
}
