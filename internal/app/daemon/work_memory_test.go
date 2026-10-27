package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

const memorySurface = "feishu:app-1:user:owner"

func memoryFixture(t *testing.T) (*App, agentproto.Command, string) {
	t.Helper()
	a := New("", "", nil, agentproto.ServerIdentity{})
	a.headlessRuntime.Paths.StateDir = t.TempDir()
	a.service.MaterializeSurface(memorySurface, "app-1", "personal", "owner")
	s := a.service.Surface(memorySurface)
	s.AttachedInstanceID = "instance"
	s.ActiveQueueItemID = "queue"
	s.QueueItems = map[string]*state.QueueItemRecord{"queue": {ID: "queue", ActorUserID: "owner", SourceKind: state.QueueItemSourceUser, SourceMessageID: "message"}}
	a.service.UpsertInstance(&state.InstanceRecord{InstanceID: "instance", WorkspaceRoot: "/tmp", WorkspaceKey: "/tmp", Online: true, Threads: map[string]*state.ThreadRecord{}})
	root := filepath.Join(a.headlessRuntime.Paths.StateDir, "work-memory")
	runtime := &workMemoryRuntime{root: root, lifecycle: a.daemonLifecycleID, busy: make(chan struct{}, 1), ioTimeout: 5 * time.Second}
	a.workMemory = runtime
	t.Cleanup(func() {
		memoryWaitIdle(t, runtime)
		if a.workMemory != nil && a.workMemory != runtime {
			memoryWaitIdle(t, a.workMemory)
		}
	})
	memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface, "active_work_ids": []string{"work"}}}, "works": map[string]any{"work": map[string]any{"context_path": "works/work/context.json"}}})
	memoryWriteContext(t, root, "记忆一", 1)
	c := agentproto.Command{CommandID: "cmd", Kind: agentproto.CommandPromptSend, Origin: agentproto.Origin{Surface: memorySurface, ChatID: "personal", UserID: "owner", MessageID: "message"}, Target: agentproto.Target{ThreadID: "thread"}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "用户原话"}}}}
	return a, c, root
}
func memoryWrite(t *testing.T, root, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, path)
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func memoryWriteContext(t *testing.T, root, text string, version int) {
	memoryWrite(t, root, "works/work/context.json", map[string]any{"schema_version": 1, "work_id": "work", "title": "事项", "owner_id": "owner", "visibility": "owner", "version": strconv.Itoa(version), "updated_at": "2026-10-27T00:00:00Z", "context": text, "dropped_count": 0})
}
func memoryDispatch(t *testing.T, a *App, c agentproto.Command) []agentproto.Command {
	t.Helper()
	var sent []agentproto.Command
	a.sendAgentCommand = func(_ string, c agentproto.Command) error { sent = append(sent, c); return nil }
	a.handleUIEvents(context.Background(), []eventcontract.Event{{Kind: eventcontract.KindAgentCommand, SurfaceSessionID: c.Origin.Surface, Command: &c}})
	return sent
}
func TestWorkMemoryInjectionRefreshAndOriginalPrompt(t *testing.T) {
	a, c, root := memoryFixture(t)
	for i, text := range []string{"记忆一", "更新后的记忆"} {
		memoryWriteContext(t, root, text, i+1)
		sent := memoryDispatch(t, a, c)
		if len(sent) != 1 || len(sent[0].Prompt.Inputs) != 2 || !strings.Contains(sent[0].Prompt.Inputs[0].Text, text) {
			t.Fatalf("memory missing: %#v", sent)
		}
		if !reflect.DeepEqual(sent[0].Prompt.Inputs[1:], c.Prompt.Inputs) {
			t.Fatal("original prompt mutated")
		}
		if !strings.Contains(sent[0].Prompt.Inputs[0].Text, "不是执行授权") {
			t.Fatal("missing source boundary")
		}
	}
}
func TestWorkMemoryIdentityGates(t *testing.T) {
	cases := map[string]func(*App, *agentproto.Command){
		"missing_origin": func(a *App, c *agentproto.Command) { c.Origin.UserID = "" },
		"wrong_origin":   func(a *App, c *agentproto.Command) { c.Origin.UserID = "other" },
		"missing_queue_actor": func(a *App, c *agentproto.Command) {
			a.service.Surface(memorySurface).QueueItems["queue"].ActorUserID = ""
		},
		"wrong_queue_actor": func(a *App, c *agentproto.Command) {
			a.service.Surface(memorySurface).QueueItems["queue"].ActorUserID = "other"
		},
		"auto": func(a *App, c *agentproto.Command) {
			a.service.Surface(memorySurface).QueueItems["queue"].SourceKind = state.QueueItemSourceAutoContinue
		},
		"helper": func(a *App, c *agentproto.Command) { c.Target.InternalHelper = true },
		"review": func(a *App, c *agentproto.Command) { c.Target.Purpose = agentproto.PromptPurposeReview },
		"steer":  func(a *App, c *agentproto.Command) { c.Kind = agentproto.CommandTurnSteer },
		"unlisted_shared": func(a *App, c *agentproto.Command) {
			c.Origin.Surface = "feishu:app-1:chat:shared"
			a.service.MaterializeSurface(c.Origin.Surface, "app-1", "shared", "owner")
			s := a.service.Surface(c.Origin.Surface)
			s.AttachedInstanceID = "instance"
			s.ActiveQueueItemID = "queue"
			s.QueueItems = a.service.Surface(memorySurface).QueueItems
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a, c, _ := memoryFixture(t)
			mutate(a, &c)
			sent := memoryDispatch(t, a, c)
			if len(sent) != 1 || !reflect.DeepEqual(sent[0].Prompt.Inputs, c.Prompt.Inputs) {
				t.Fatalf("unauthorized injection: %#v", sent)
			}
		})
	}
}
func TestWorkMemoryBadSnapshotPreservesMessage(t *testing.T) {
	for _, kind := range []string{"json", "oversize", "symlink", "traversal", "owner", "work_id", "missing_manifest"} {
		t.Run(kind, func(t *testing.T) {
			a, c, root := memoryFixture(t)
			p := filepath.Join(root, "works/work/context.json")
			switch kind {
			case "json":
				os.WriteFile(p, []byte("{"), 0600)
			case "oversize":
				memoryWriteContext(t, root, strings.Repeat("x", 70000), 2)
			case "symlink":
				os.Remove(p)
				os.Symlink(filepath.Join(root, "manifest.json"), p)
			case "traversal":
				memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface, "active_work_ids": []string{"work"}}}, "works": map[string]any{"work": map[string]any{"context_path": "../outside.json"}}})
			case "owner", "work_id":
				b, _ := os.ReadFile(p)
				var m map[string]any
				json.Unmarshal(b, &m)
				key := kind
				if kind == "owner" {
					key = "owner_id"
				}
				m[key] = "wrong"
				memoryWrite(t, root, "works/work/context.json", m)
			case "missing_manifest":
				os.Remove(filepath.Join(root, "manifest.json"))
			}
			sent := memoryDispatch(t, a, c)
			if len(sent) != 1 || !reflect.DeepEqual(sent[0].Prompt.Inputs, c.Prompt.Inputs) {
				t.Fatalf("bad snapshot changed prompt: %#v", sent)
			}
		})
	}
}
func TestWorkMemoryPrivateThreadCannotCrossSurfaceAfterRestart(t *testing.T) {
	a, c, root := memoryFixture(t)
	if got := memoryDispatch(t, a, c); len(got) != 1 || len(got[0].Prompt.Inputs) != 2 {
		t.Fatal("initial injection missing")
	}
	a.workMemory = nil
	// Removing the manifest must not remove protection of already-injected history.
	os.Remove(filepath.Join(root, "manifest.json"))
	other := "feishu:app-1:chat:shared"
	a.service.MaterializeSurface(other, "app-1", "shared", "owner")
	a.service.Surface(other).AttachedInstanceID = "instance"
	c.Origin.Surface = other
	c.Origin.ChatID = "shared"
	if got := memoryDispatch(t, a, c); len(got) != 0 {
		t.Fatal("private thread leaked to another surface")
	}
}
func TestWorkMemoryNewThreadFirstTurnAndPendingIsolation(t *testing.T) {
	a, c, root := memoryFixture(t)
	a.service.Instance("instance").Threads["normal"] = &state.ThreadRecord{ThreadID: "normal"}

	s := a.service.Surface(memorySurface)
	s.ActiveQueueItemID = ""
	s.QueueItems = map[string]*state.QueueItemRecord{}
	s.RouteMode = state.RouteModeNewThreadReady
	s.PreparedThreadCWD = "/tmp"
	events := a.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, GatewayID: "app-1", SurfaceSessionID: memorySurface, ChatID: "personal", ActorUserID: "owner", MessageID: "message", Text: "用户原话"})
	found := false
	for _, e := range events {
		if e.Command != nil && e.Command.Kind == agentproto.CommandPromptSend {
			c = *e.Command
			c.CommandID = "cmd"
			found = true
		}
	}
	if !found {
		t.Fatalf("no real pending prompt: %#v", events)
	}
	if got := memoryDispatch(t, a, c); len(got) != 1 || len(got[0].Prompt.Inputs) != 2 {
		t.Fatal("new thread missed memory")
	}
	other := "feishu:app-1:chat:shared"
	a.service.MaterializeSurface(other, "app-1", "shared", "other")
	a.service.Surface(other).AttachedInstanceID = "instance"
	stranger := c
	stranger.CommandID = "other"
	stranger.Origin.Surface = other
	stranger.Origin.ChatID = "shared"
	stranger.Origin.UserID = "other"
	stranger.Target.ThreadID = "normal"
	if got := memoryDispatch(t, a, stranger); len(got) != 1 {
		t.Fatal("known normal thread blocked")
	}
	stranger.Target.ThreadID = ""
	if got := memoryDispatch(t, a, stranger); len(got) != 1 {
		t.Fatal("unrelated new thread blocked")
	}
	stranger.Target.ThreadID = "new-private"
	if got := memoryDispatch(t, a, stranger); len(got) != 0 {
		t.Fatal("pending unknown thread leaked")
	}
	a.mu.Lock()
	ok := a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "new-private", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}})
	a.mu.Unlock()
	if !ok {
		t.Fatal("failed recording private thread")
	}
	a.mu.Lock()
	a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventThreadDiscovered, CommandID: "cmd", ThreadID: "new-private", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: memorySurface}})
	a.mu.Unlock()
	b, _ := os.ReadFile(filepath.Join(root, "private-threads.json"))
	var owners workMemoryOwners
	json.Unmarshal(b, &owners)
	if _, ok := owners.Threads["new-private"]; !ok {
		t.Fatal("actual turn.started did not resolve pending ownership")
	}
	a.workMemory = nil
	if got := memoryDispatch(t, a, stranger); len(got) != 0 {
		t.Fatal("persisted new thread leaked")
	}
}
func TestWorkMemoryHistoryCannotBypassPrivateOwnership(t *testing.T) {
	a, c, _ := memoryFixture(t)
	memoryDispatch(t, a, c)
	for _, other := range []bool{false, true} {
		surface := memorySurface
		if other {
			surface = "feishu:app-1:chat:shared"
			a.service.MaterializeSurface(surface, "app-1", "shared", "owner")
			a.service.Surface(surface).AttachedInstanceID = "instance"
		}
		sent := 0
		a.sendAgentCommand = func(_ string, _ agentproto.Command) error { sent++; return nil }
		a.handleThreadHistoryDaemonCommand(control.DaemonCommand{SurfaceSessionID: surface, InstanceID: "instance", ThreadID: "thread", SourceMessageID: "history"})
		if other && sent != 0 {
			t.Fatal("history bypassed private ownership")
		}
		if !other && sent != 1 {
			t.Fatal("owner cannot read own history")
		}
	}
}
func TestWorkMemoryOutputCannotCrossSurface(t *testing.T) {
	a, c, _ := memoryFixture(t)
	memoryDispatch(t, a, c)
	other := "feishu:app-1:chat:shared"
	a.service.MaterializeSurface(other, "app-1", "shared", "owner")
	for _, surface := range []string{memorySurface, other} {
		a.mu.Lock()
		ok := a.workMemoryUIEventAllowedLocked(eventcontract.Event{SurfaceSessionID: surface, Block: &render.Block{ThreadID: "thread", Text: "private output"}})
		a.mu.Unlock()
		if ok != (surface == memorySurface) {
			t.Fatalf("output access surface=%s allowed=%t", surface, ok)
		}
	}
}
func TestWorkMemoryDoesNotBlockOwnerStop(t *testing.T) {
	a, c, _ := memoryFixture(t)
	memoryDispatch(t, a, c)
	c.Kind = agentproto.CommandTurnInterrupt
	c.Origin.MessageID = "stop"
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("private ownership blocks stop")
	}
}
func TestWorkMemoryNewLifecycleDoesNotConsumeOldPending(t *testing.T) {
	a, c, root := memoryFixture(t)
	c.Target.ThreadID = ""
	memoryDispatch(t, a, c)
	a.daemonLifecycleID = "next-lifecycle"
	a.workMemory = nil
	a.mu.Lock()
	a.noteWorkMemoryEventLocked("instance", agentproto.Event{Kind: agentproto.EventTurnStarted, CommandID: c.CommandID, ThreadID: "unrelated"})
	a.mu.Unlock()
	b, _ := os.ReadFile(filepath.Join(root, "private-threads.json"))
	var owners workMemoryOwners
	json.Unmarshal(b, &owners)
	if len(owners.Pending) != 1 || len(owners.Threads) != 0 {
		t.Fatal("restart reused old command ID")
	}
}

func TestWorkMemoryLimitedScopesNeverInjectEvenWhenAllowlisted(t *testing.T) {
	for _, kind := range []string{"execution", "surface_execution", "encoded", "configured"} {
		t.Run(kind, func(t *testing.T) {
			a, c, root := memoryFixture(t)
			switch kind {
			case "execution":
				c.Overrides.LimitedExecution = &limitedgroups.ExecutionPolicy{}
			case "surface_execution":
				a.service.Surface(memorySurface).LimitedExecution = &limitedgroups.ExecutionPolicy{}
			case "encoded", "configured":
				surface := "feishu:app-1:chat:limited"
				if kind == "encoded" {
					surface = gatewaypkg.LimitedSurfaceID("app-1", "limited", "ou_owner", "topic")
				}
				a.service.MaterializeSurface(surface, "app-1", "limited", "owner")
				target := a.service.Surface(surface)
				source := a.service.Surface(memorySurface)
				target.AttachedInstanceID = source.AttachedInstanceID
				target.QueueItems = source.QueueItems
				target.ActiveQueueItemID = source.ActiveQueueItemID
				c.Origin.Surface = surface
				c.Origin.ChatID = "limited"
				memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": surface, "active_work_ids": []string{"work"}}}, "works": map[string]any{"work": map[string]any{"context_path": "works/work/context.json"}}})
				if kind == "configured" {
					a.admin.loadConfig = func() (config.LoadedAppConfig, error) {
						return config.LoadedAppConfig{Config: config.AppConfig{Feishu: config.FeishuSettings{Apps: []config.FeishuAppConfig{{ID: "app-1", LimitedGroups: []limitedgroups.Policy{{ChatID: "limited", Enabled: true, OwnerUserID: "owner"}}}}}}}, nil
					}
				}
			}
			a.mu.Lock()
			got, err := a.prepareWorkMemoryCommandLocked(c.Origin.Surface, "instance", c)
			a.mu.Unlock()
			if err != nil || !reflect.DeepEqual(got.Prompt.Inputs, c.Prompt.Inputs) {
				t.Fatal("limited scope injected")
			}
		})
	}
}

func TestWorkMemoryBrokenPrivatePendingDoesNotSwallowOrdinaryNewTurn(t *testing.T) {
	a, c, root := memoryFixture(t)
	c.Target.ThreadID = ""
	memoryDispatch(t, a, c)
	// A broken ownership write/read cannot justify opening private histories.
	os.WriteFile(filepath.Join(root, "private-threads.json"), []byte("{"), 0600)
	surface := "feishu:app-1:user:other"
	a.service.MaterializeSurface(surface, "app-1", "other-chat", "other")
	a.service.UpsertInstance(&state.InstanceRecord{InstanceID: "ordinary", Online: true, WorkspaceRoot: "/tmp", WorkspaceKey: "/tmp", Threads: map[string]*state.ThreadRecord{}})
	s := a.service.Surface(surface)
	s.AttachedInstanceID = "ordinary"
	s.RouteMode = state.RouteModeNewThreadReady
	s.PreparedThreadCWD = "/tmp"
	events := a.service.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, GatewayID: "app-1", SurfaceSessionID: surface, ChatID: "other-chat", ActorUserID: "other", MessageID: "normal-message", Text: "普通新任务"})
	var normal agentproto.Command
	for _, e := range events {
		if e.Command != nil && e.Command.Kind == agentproto.CommandPromptSend {
			normal = *e.Command
			normal.CommandID = "ordinary-cmd"
		}
	}
	if normal.Kind == "" {
		t.Fatal("no ordinary new prompt")
	}
	if got := memoryDispatch(t, a, normal); len(got) != 1 || len(got[0].Prompt.Inputs) != 1 {
		t.Fatal("ordinary new prompt blocked")
	}
	event := agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: "ordinary-new", TurnID: "normal-turn", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: surface}}
	a.mu.Lock()
	a.noteWorkMemoryEventLocked("ordinary", agentproto.Event{Kind: agentproto.EventThreadDiscovered, CommandID: "ordinary-cmd", ThreadID: "ordinary-new", Initiator: event.Initiator})
	ok := a.noteWorkMemoryEventLocked("ordinary", event)
	if ok {
		a.service.ApplyAgentEvent("ordinary", event)
	}
	a.mu.Unlock()
	if !ok {
		t.Fatal("ordinary new turn.started swallowed by broken private registry")
	}
	a.mu.Lock()
	allowed := a.workMemoryUIEventAllowedLocked(eventcontract.Event{SurfaceSessionID: surface, Block: &render.Block{ThreadID: "ordinary-new", Text: "普通结果"}})
	a.mu.Unlock()
	if !allowed {
		t.Fatal("ordinary new output swallowed by private pending")
	}
	a.mu.Lock()
	deltaAllowed := a.noteWorkMemoryEventLocked("ordinary", agentproto.Event{Kind: agentproto.EventItemDelta, CommandID: "ordinary-cmd", ThreadID: "ordinary-new", TurnID: "normal-turn", Delta: "普通结果"})
	a.mu.Unlock()
	if !deltaAllowed {
		t.Fatal("ordinary native delta swallowed by broken registry")
	}
	normal.Target.ThreadID = "unknown-private"
	normal.CommandID = "resume-unknown"
	if got := memoryDispatch(t, a, normal); len(got) != 0 {
		t.Fatal("unproven resume opened private pending")
	}
}
func TestWorkMemoryBudgetIncludesUTF8HeaderAndProvenance(t *testing.T) {
	a, c, root := memoryFixture(t)
	memoryWriteContext(t, root, strings.Repeat("x", workMemoryBudget-10), 1)
	got := memoryDispatch(t, a, c)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Prompt.Inputs, c.Prompt.Inputs) {
		t.Fatal("header pushed memory past byte budget")
	}
}
func TestWorkMemoryThreadBindingAndSwitchStayInsideSurfaceAllowlist(t *testing.T) {
	a, c, root := memoryFixture(t)
	memoryWrite(t, root, "works/second/context.json", map[string]any{"schema_version": 1, "work_id": "second", "title": "第二事项", "owner_id": "owner", "visibility": "owner", "version": "1", "updated_at": "2026-10-27T00:00:00Z", "context": "第二事项资料"})
	manifest := map[string]any{"schema_version": 1, "enabled": true, "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface, "active_work_ids": []string{"work"}, "thread_bindings": map[string]any{"thread": []string{"second"}}}}, "works": map[string]any{"work": map[string]any{"context_path": "works/work/context.json"}, "second": map[string]any{"context_path": "works/second/context.json"}}}
	memoryWrite(t, root, "manifest.json", manifest)
	got := memoryDispatch(t, a, c)
	if len(got) != 1 || len(got[0].Prompt.Inputs) != 2 || !strings.Contains(got[0].Prompt.Inputs[0].Text, "第二事项资料") {
		t.Fatal("thread-specific work not selected")
	}
	manifest["bindings"] = []any{map[string]any{"surface_id": memorySurface, "active_work_ids": []string{"work"}}}
	memoryWrite(t, root, "manifest.json", manifest)
	got = memoryDispatch(t, a, c)
	if len(got) != 1 || !strings.Contains(got[0].Prompt.Inputs[0].Text, "记忆一") {
		t.Fatal("active work switch not refreshed")
	}
	other := "feishu:app-1:chat:shared"
	a.service.MaterializeSurface(other, "app-1", "shared", "owner")
	s := a.service.Surface(other)
	s.AttachedInstanceID = "instance"
	s.ActiveQueueItemID = "queue"
	s.QueueItems = a.service.Surface(memorySurface).QueueItems
	c.Origin.Surface = other
	c.Origin.ChatID = "shared"
	if got = memoryDispatch(t, a, c); len(got) != 0 {
		t.Fatal("thread binding bypassed surface gate")
	}
}
func TestWorkMemoryForkClaimsNewExecutionThread(t *testing.T) {
	a, c, root := memoryFixture(t)
	c.Target.ExecutionMode = agentproto.PromptExecutionModeForkEphemeral
	c.Target.SourceThreadID = "source"
	got := memoryDispatch(t, a, c)
	if len(got) != 1 || len(got[0].Prompt.Inputs) != 2 {
		t.Fatal("fork injection missing")
	}
	b, _ := os.ReadFile(filepath.Join(root, "private-threads.json"))
	var owners workMemoryOwners
	json.Unmarshal(b, &owners)
	if len(owners.Pending) != 1 || len(owners.Threads) != 0 {
		t.Fatal("fork tainted the source instead of tracking new execution")
	}
}
func TestWorkMemoryPrivateForkRetainsOwnershipWithoutFreshSnapshot(t *testing.T) {
	a, c, root := memoryFixture(t)
	memoryDispatch(t, a, c)
	os.Remove(filepath.Join(root, "manifest.json"))
	c.CommandID = "fork"
	c.Target.ExecutionMode = agentproto.PromptExecutionModeForkEphemeral
	c.Target.SourceThreadID = "thread"
	c.Target.ThreadID = ""
	got := memoryDispatch(t, a, c)
	if len(got) != 1 || len(got[0].Prompt.Inputs) != 1 {
		t.Fatal("fork lost original input or injected disabled snapshot")
	}
	b, _ := os.ReadFile(filepath.Join(root, "private-threads.json"))
	var owners workMemoryOwners
	json.Unmarshal(b, &owners)
	if len(owners.Pending) != 1 {
		t.Fatal("private history fork lost privacy when manifest disabled")
	}
}

func memoryWaitIdle(t *testing.T, runtime *workMemoryRuntime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.proofMu.Lock()
		if !runtime.drainingRecords {
			select {
			case runtime.busy <- struct{}{}:
				runtime.proofMu.Unlock()
				<-runtime.busy
				return
			default:
			}
		}
		runtime.proofMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Error("memory I/O did not settle before fixture cleanup")
}
