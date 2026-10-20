package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestLimitedGroupsStartupDoesNotAnnounceInternalRouteChanges(t *testing.T) {
	now := time.Now()
	svc := newServiceForTest(&now)
	p := limitedServicePolicy(t)
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: "private-wrapper", WorkspaceRoot: p.Workdir, WorkspaceKey: p.Workdir, Online: true, Managed: true, Backend: agentproto.BackendCodex, CapabilitiesDeclared: true, Capabilities: agentproto.Capabilities{LimitedGroups: true}, Threads: map[string]*state.ThreadRecord{}})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "limited", InstanceID: "private-wrapper", ActorUserID: "ou_owner"})
	surface := svc.root.Surfaces["limited"]
	surface.LimitedExecution = p
	// Automatic private-workspace attach prepares the original queued question;
	// it must not tell the user to send a second question or reset context.
	surface.AttachedInstanceID = ""
	events := svc.attachWorkspaceWithOptions(surface, p.Workdir, attachWorkspaceOptions{PrepareNewThread: true})
	if surface.RouteMode != state.RouteModeNewThreadReady || surface.PreparedThreadCWD != p.Workdir {
		t.Fatalf("private route not ready: %+v", surface)
	}
	for _, e := range events {
		if e.Notice != nil {
			t.Errorf("internal attach emitted notice: %s", e.Notice.Code)
		}
	}
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: "limited", ActorUserID: "ou_owner", MessageID: "original-question", Text: "query actual project", LimitedExecution: p})
	commands := limitedServiceCommands(events)
	if len(commands) != 1 || commands[0].Kind != agentproto.CommandPromptSend || commands[0].Target.CWD != p.Workdir {
		t.Fatalf("original question not dispatched once: %+v", commands)
	}
	// First native thread binding still updates selection state without a target card.
	events = svc.threadSelectionEvents(surface, "created-private-thread", string(state.RouteModePinned), "private question")
	if surface.LastSelection == nil || surface.LastSelection.ThreadID != "created-private-thread" {
		t.Fatal("selection bookkeeping lost")
	}
	if len(events) != 0 {
		t.Fatal("private native thread binding emitted target card")
	}
}

func TestLimitedGroupsQuietBootstrapStillReportsRealBlocker(t *testing.T) {
	svc, surface, _ := limitedServiceRequest(t, "shell")
	events := svc.prepareNewThreadWithNotices(surface, surfaceOverlayRouteCleanupOptions{}, false)
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "request_pending" {
		t.Fatalf("real owner gate hidden: %+v", events)
	}
}
