package daemon

import (
	"context"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditMissingOwnershipMustNotExposePrivateThread(t *testing.T) {
	a, c, root := memoryFixture(t)
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("initial dispatch failed")
	}
	if err := os.Remove(filepath.Join(root, "private-threads.json")); err != nil {
		t.Fatal(err)
	}
	other := "feishu:app-1:user:other"
	a.service.MaterializeSurface(other, "app-1", "other-chat", "other")
	a.service.Surface(other).AttachedInstanceID = "instance"
	a.mu.Lock()
	err := a.guardWorkMemoryHistoryLocked(other, "instance", "thread")
	a.mu.Unlock()
	if err == nil {
		t.Fatal("BUG: deleted registry allowed non-owner to read previously injected private thread")
	}
}
func TestAuditBusyMustNotDropStartedLifecycle(t *testing.T) {
	a, c, _ := memoryFixture(t)
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("initial dispatch failed")
	}
	a.workMemory.busy <- struct{}{}
	a.mu.Lock()
	ok := a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "thread", TurnID: "real-turn", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}})
	a.mu.Unlock()
	<-a.workMemory.busy
	if !ok {
		t.Fatal("BUG: optional memory reader busy discarded actual turn.started with no deferred replay")
	}
}

func TestWorkMemoryBusyStillAdvancesCoreLifecycleAndQuarantinesPrivateThread(t *testing.T) {
	a, _, root := memoryFixture(t)
	s := a.service.Surface(memorySurface)
	s.ActiveQueueItemID = ""
	s.QueueItems = map[string]*state.QueueItemRecord{}
	s.RouteMode = state.RouteModeNewThreadReady
	s.PreparedThreadCWD = "/tmp"
	events := a.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, GatewayID: "app-1", SurfaceSessionID: memorySurface, ChatID: "personal", ActorUserID: "owner", MessageID: "real-msg", Text: "start"})
	var command agentproto.Command
	for _, e := range events {
		if e.Command != nil {
			command = *e.Command
			command.CommandID = "private-new"
		}
	}
	if got := memoryDispatch(t, a, command); len(got) != 1 || len(got[0].Prompt.Inputs) != 2 {
		t.Fatal("private new not injected")
	}
	a.workMemory.busy <- struct{}{}
	a.onEvents(context.Background(), "instance", []agentproto.Event{{Kind: agentproto.EventThreadDiscovered, CommandID: command.CommandID, ThreadID: "private-created", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}}, {Kind: agentproto.EventTurnStarted, ThreadID: "private-created", TurnID: "private-turn", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}}})
	if a.service.Instance("instance").ActiveTurnID != "private-turn" {
		t.Fatal("core never processed real turn.started")
	}
	other := "feishu:app-1:user:other"
	a.service.MaterializeSurface(other, "app-1", "other-chat", "other")
	a.mu.Lock()
	err := a.guardWorkMemoryHistoryLocked(other, "instance", "private-created")
	a.mu.Unlock()
	if err == nil {
		t.Fatal("unresolved private thread exposed")
	}
	<-a.workMemory.busy
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r, e := openWorkMemoryRoot(root)
		if e == nil {
			o, e := readWorkMemoryOwners(r)
			r.Close()
			if e == nil && o.Threads["private-created"].Surface == memorySurface {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	r, e := openWorkMemoryRoot(root)
	if e != nil {
		t.Fatal(e)
	}
	o, e := readWorkMemoryOwners(r)
	r.Close()
	if e != nil || o.Threads["private-created"].Surface != memorySurface {
		t.Fatal("exact deferred correlation never persisted")
	}
	a.onEvents(context.Background(), "instance", []agentproto.Event{{Kind: agentproto.EventTurnCompleted, ThreadID: "private-created", TurnID: "private-turn", Status: "completed", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}}})
	if a.service.Instance("instance").ActiveTurnID != "" || len(a.service.PendingRemoteTurns()) != 0 {
		t.Fatal("core never settled turn.completed")
	}
}
func TestWorkMemoryNeverEnabledCollectorDirectoryPreservesNormalDispatch(t *testing.T) {
	a, c, root := memoryFixture(t)
	os.Remove(filepath.Join(root, "manifest.json"))
	if got := memoryDispatch(t, a, c); len(got) != 1 || len(got[0].Prompt.Inputs) != 1 {
		t.Fatal("collector directory blocked ordinary prompt")
	}
	if _, err := os.Stat(filepath.Join(root, "private-threads.json")); !os.IsNotExist(err) {
		t.Fatal("ordinary prompt initialized private ownership")
	}
}
func TestWorkMemoryActivationSurvivesMissingRegistryOrCollectorRoot(t *testing.T) {
	for _, missing := range []string{"registry", "collector-root"} {
		t.Run(missing, func(t *testing.T) {
			a, c, root := memoryFixture(t)
			if got := memoryDispatch(t, a, c); len(got) != 1 || len(got[0].Prompt.Inputs) != 2 {
				t.Fatal("initial injection missing")
			}
			marker := filepath.Join(filepath.Dir(root), workMemoryActivationFile)
			if stat, err := os.Stat(marker); err != nil || stat.Mode().Perm() != 0600 {
				t.Fatal("activation not durably private")
			}
			if missing == "registry" {
				os.Remove(filepath.Join(root, "private-threads.json"))
			} else {
				os.RemoveAll(root)
			}
			a.workMemory = nil
			other := "feishu:app-1:user:other"
			a.service.MaterializeSurface(other, "app-1", "other-chat", "other")
			a.mu.Lock()
			err := a.guardWorkMemoryHistoryLocked(other, "instance", "thread")
			a.mu.Unlock()
			if err == nil {
				t.Fatal("restart interpreted lost ownership as never enabled")
			}
		})
	}
}
func TestWorkMemoryNeverBindsPrivateThreadByCurrentSurfacePending(t *testing.T) {
	a, c, root := memoryFixture(t)
	c.Target.ThreadID = ""
	memoryDispatch(t, a, c)
	a.mu.Lock()
	a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "stale-thread", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}})
	a.mu.Unlock()
	r, err := openWorkMemoryRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := readWorkMemoryOwners(r)
	r.Close()
	if err != nil || len(owners.Threads) != 0 || len(owners.Pending) != 1 {
		t.Fatal("missing command correlation consumed private pending")
	}
	a.mu.Lock()
	a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventThreadDiscovered, CommandID: "old-command", ThreadID: "stale-thread", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}})
	a.mu.Unlock()
	r, err = openWorkMemoryRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	owners, err = readWorkMemoryOwners(r)
	r.Close()
	if err != nil || len(owners.Threads) != 0 || len(owners.Pending) != 1 {
		t.Fatal("old command consumed new private pending")
	}
}
