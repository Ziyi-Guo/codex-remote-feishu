package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/testutil"
)

func TestTargetPickerKnownWorkspaceAcceptsRecoverableButMismatchedVisibleWorkspace(t *testing.T) {
	now := time.Date(2026, 5, 1, 15, 5, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	workspaceRoot := t.TempDir()
	svc.MaterializeSurfaceResume("surface-1", "", "chat-1", "user-1", state.ProductModeNormal, agentproto.BackendClaude, "profile-a", "", "")
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:      "inst-claude",
		DisplayName:     "repo",
		WorkspaceRoot:   workspaceRoot,
		WorkspaceKey:    workspaceRoot,
		ShortName:       "repo",
		Backend:         agentproto.BackendClaude,
		ClaudeProfileID: "profile-b",
		Online:          true,
		Threads:         map[string]*state.ThreadRecord{},
	})

	view := singleTargetPickerEvent(t, svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionListInstances,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
	}))
	addMode := openAddWorkspaceLocalDirectoryPage(t, svc, view)
	surface := svc.root.Surfaces["surface-1"]
	record := svc.activeTargetPicker(surface)
	record.LocalDirectoryPath = workspaceRoot

	checkEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTargetPickerConfirm,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		PickerID:         addMode.PickerID,
	})
	if len(checkEvents) == 0 || checkEvents[0].TargetPickerView == nil {
		t.Fatalf("expected owner-card checked state for recoverable workspace, got %#v", checkEvents)
	}
	if got := checkEvents[0].TargetPickerView; !got.LocalDirectoryChecked || got.ConfirmLabel != "接入并继续" {
		t.Fatalf("expected recoverable workspace to require explicit second confirm, got %#v", got)
	}

	confirmEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTargetPickerConfirm,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		PickerID:         addMode.PickerID,
	})

	if surface.PendingHeadless == nil || !surface.PendingHeadless.PrepareNewThread || !testutil.SamePath(surface.PendingHeadless.ThreadCWD, workspaceRoot) {
		t.Fatalf("expected recoverable mismatched workspace to be treated as known and continue via new-thread preparation, got %#v", surface)
	}
	if len(confirmEvents) == 0 || confirmEvents[0].TargetPickerView == nil {
		t.Fatalf("expected owner-card processing state for recoverable workspace, got %#v", confirmEvents)
	}
	if got := confirmEvents[0].TargetPickerView; got.Stage != control.FeishuTargetPickerStageProcessing || got.StatusTitle != "正在接入工作区" {
		t.Fatalf("expected recoverable known workspace to enter processing, got %#v", got)
	}
	var sawStart bool
	for _, event := range confirmEvents {
		if event.DaemonCommand != nil && event.DaemonCommand.Kind == control.DaemonCommandStartHeadless {
			sawStart = true
			break
		}
	}
	if !sawStart {
		t.Fatalf("expected recoverable known workspace to start matching headless, got %#v", confirmEvents)
	}
}

func TestTargetPickerNewThreadStartsHeadlessWhenParentInstanceIsClaimed(t *testing.T) {
	now := time.Date(2026, 9, 30, 3, 32, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-parent",
		WorkspaceRoot: "/data/dl",
		WorkspaceKey:  "/data/dl",
		Backend:       agentproto.BackendCodex,
		Online:        true,
		Managed:       true,
		Source:        "headless",
		Threads: map[string]*state.ThreadRecord{
			"thread-parent": {ThreadID: "thread-parent", CWD: "/data/dl"},
			"thread-child":  {ThreadID: "thread-child", CWD: "/data/dl/ipgen"},
		},
	})
	ownerEvents := svc.ApplySurfaceAction(control.Action{
		Kind: control.ActionAttachWorkspace, SurfaceSessionID: "surface-owner", ChatID: "chat-owner",
		ActorUserID: "user-owner", WorkspaceKey: "/data/dl",
	})
	if noticeCode(ownerEvents, "workspace_attached") == "" {
		t.Fatalf("owner did not attach parent workspace: %#v", ownerEvents)
	}

	view := singleTargetPickerEvent(t, svc.ApplySurfaceAction(control.Action{
		Kind: control.ActionListInstances, SurfaceSessionID: "surface-target", ChatID: "chat-target", ActorUserID: "user-target",
	}))
	selected := svc.ApplySurfaceAction(control.Action{
		Kind: control.ActionTargetPickerSelectWorkspace, SurfaceSessionID: "surface-target", ChatID: "chat-target",
		ActorUserID: "user-target", PickerID: view.PickerID, WorkspaceKey: "/data/dl/ipgen",
	})
	view = targetPickerFromEvent(t, selected[0])
	if view.SelectedSessionValue != targetPickerNewThreadValue {
		t.Fatalf("expected new-thread choice for child workspace, got %#v", view)
	}
	confirmed := svc.ApplySurfaceAction(control.Action{
		Kind: control.ActionTargetPickerConfirm, SurfaceSessionID: "surface-target", ChatID: "chat-target",
		ActorUserID: "user-target", PickerID: view.PickerID,
	})
	if !hasStartHeadlessCommand(confirmed) {
		t.Fatalf("expected independent child headless instead of occupied parent instance, got %#v", confirmed)
	}
	target := svc.root.Surfaces["surface-target"]
	if target.PendingHeadless == nil || target.PendingHeadless.WorkspaceKey != "/data/dl/ipgen" || !target.PendingHeadless.PrepareNewThread {
		t.Fatalf("target did not enter child new-thread startup: %#v", target)
	}
	if owner := svc.root.Surfaces["surface-owner"]; owner.AttachedInstanceID != "inst-parent" {
		t.Fatalf("existing parent attachment changed: %#v", owner)
	}
}
