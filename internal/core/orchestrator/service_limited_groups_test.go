package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func limitedServicePolicy(t *testing.T) *limitedgroups.ExecutionPolicy {
	t.Helper()
	root := t.TempDir()
	return &limitedgroups.ExecutionPolicy{OwnerUserID: "ou_owner", Workspace: filepath.Join(root, "source"), Workdir: filepath.Join(root, "work"), OutputDir: filepath.Join(root, "work", "output")}
}

func TestLimitedGroupsDiscoveryKeepsProjectionWithoutGlobalRefresh(t *testing.T) {
	now := time.Now()
	svc := newServiceForTest(&now)
	p := limitedServicePolicy(t)
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: "private-wrapper", WorkspaceRoot: p.Workdir, WorkspaceKey: p.Workdir, Online: true, Managed: true, Backend: agentproto.BackendCodex, CapabilitiesDeclared: true, Capabilities: agentproto.Capabilities{ThreadsRefresh: true, LimitedGroups: true}, Threads: map[string]*state.ThreadRecord{}})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-limited", InstanceID: "private-wrapper", ChatID: "oc_limited", ActorUserID: "ou_owner"})
	surface := svc.root.Surfaces["surface-limited"]
	surface.LimitedExecution = p
	events := svc.ApplyAgentEvent("private-wrapper", agentproto.Event{Kind: agentproto.EventThreadDiscovered, ThreadID: "new-private-thread", CWD: p.Workdir, FocusSource: "remote_created_thread"})
	for _, c := range limitedServiceCommands(events) {
		if c.Kind == agentproto.CommandThreadsRefresh {
			t.Fatal("private discovery attempted owner thread/list")
		}
	}
	thread := svc.root.Instances["private-wrapper"].Threads["new-private-thread"]
	if thread == nil || thread.CWD != p.Workdir || !thread.Loaded || surface.AttachedInstanceID != "private-wrapper" {
		t.Fatal("private thread projection or binding was lost")
	}
}

func limitedServiceRequest(t *testing.T, kind string) (*Service, *state.SurfaceConsoleRecord, *limitedgroups.ExecutionPolicy) {
	t.Helper()
	now := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)
	surface := svc.root.Surfaces["surface-1"]
	policy := limitedServicePolicy(t)
	surface.LimitedExecution = policy
	event := agentproto.Event{Kind: agentproto.EventRequestStarted, ThreadID: "thread-1", TurnID: "turn-1", RequestID: "limited-request"}
	switch kind {
	case "shell":
		event.Metadata = map[string]any{"requestType": "approval", "requestKind": "approval_command", "command": "touch output.txt"}
	case "mcp":
		event.RequestPrompt = mcpApprovalRequestPrompt([]any{"session", "always"})
		event.Metadata = map[string]any{"requestType": "mcp_server_elicitation"}
	case "permission":
		event.RequestPrompt = &agentproto.RequestPrompt{Type: agentproto.RequestTypePermissionsRequestApproval, Permissions: &agentproto.PermissionsRequestPrompt{Permissions: []map[string]any{{"name": "network"}}}}
		event.Metadata = map[string]any{"requestType": "permissions_request_approval"}
	default:
		t.Fatal("unknown fixture kind")
	}
	svc.ApplyAgentEvent("inst-1", event)
	if pendingRequestRecord(surface, "limited-request") == nil {
		t.Fatal("native request was not recorded")
	}
	return svc, surface, policy
}

func limitedServiceCommands(events []eventcontract.Event) []agentproto.Command {
	var commands []agentproto.Command
	for _, event := range events {
		if event.Command != nil {
			commands = append(commands, *event.Command)
		}
	}
	return commands
}

func limitedServiceResponse(surface *state.SurfaceConsoleRecord, policy *limitedgroups.ExecutionPolicy, actor, requestType, option string) control.Action {
	return control.Action{Kind: control.ActionRespondRequest, SurfaceSessionID: surface.SurfaceSessionID, ActorUserID: actor, LimitedExecution: policy, Request: testRequestAction("limited-request", requestType, option, nil, 0)}
}

func TestLimitedGroupsServicePanCannotApproveNativeRequestsOrForgeType(t *testing.T) {
	for _, kind := range []string{"shell", "mcp", "permission"} {
		for _, forged := range []bool{false, true} {
			name := kind
			if forged {
				name += "/forged-user-input"
			}
			t.Run(name, func(t *testing.T) {
				svc, surface, policy := limitedServiceRequest(t, kind)
				record := pendingRequestRecord(surface, "limited-request")
				requestType := record.RequestType
				if forged {
					requestType = "request_user_input"
				}
				events := svc.ApplySurfaceAction(limitedServiceResponse(surface, policy, "ou_pan", requestType, "accept"))
				if commands := limitedServiceCommands(events); len(commands) != 0 {
					t.Fatalf("non-owner approved native %s request: %d commands", kind, len(commands))
				}
				if requestLifecycleBlocksInteractiveResponse(record) {
					t.Fatal("non-owner consumed native request")
				}
			})
		}
	}
}

func TestLimitedGroupsServiceOwnerCanAcceptOnlyOnce(t *testing.T) {
	for _, kind := range []string{"shell", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			svc, surface, policy := limitedServiceRequest(t, kind)
			record := pendingRequestRecord(surface, "limited-request")
			action := limitedServiceResponse(surface, policy, "ou_owner", record.RequestType, "accept")
			// A forged input type must not select a different response builder.
			action.Request.RequestType = "request_user_input"
			action.Request.Answers = map[string][]string{"scope": {"session"}, "persist": {"always"}}
			commands := limitedServiceCommands(svc.ApplySurfaceAction(action))
			if len(commands) != 1 || commands[0].Kind != agentproto.CommandRequestRespond {
				t.Fatalf("owner accept once did not dispatch exactly once: %+v", commands)
			}
			response := commands[0].Request.Response
			if response["scope"] == "session" || response["scope"] == "always" {
				t.Fatalf("accept acquired persistent scope: %+v", response)
			}
			if meta, ok := response["_meta"].(map[string]any); ok && meta["persist"] != nil {
				t.Fatalf("accept acquired persistent MCP grant: %+v", meta)
			}
			if kind == "shell" && response["decision"] != "accept" {
				t.Fatalf("forged type changed shell response builder: %+v", response)
			}
			if kind == "mcp" && response["action"] != "accept" {
				t.Fatalf("forged type changed MCP response builder: %+v", response)
			}
			if repeated := limitedServiceCommands(svc.ApplySurfaceAction(action)); len(repeated) != 0 {
				t.Fatal("replayed owner callback dispatched twice")
			}
		})
	}
}

func TestLimitedGroupsServiceRejectsSessionAndPersistentGrants(t *testing.T) {
	for _, kind := range []string{"shell", "mcp", "permission"} {
		for _, option := range []string{"acceptForSession", "acceptAlways", "always"} {
			t.Run(kind+"/"+option, func(t *testing.T) {
				svc, surface, policy := limitedServiceRequest(t, kind)
				record := pendingRequestRecord(surface, "limited-request")
				for _, option := range record.Options {
					if id := control.NormalizeRequestOptionID(option.OptionID); id == "acceptForSession" || id == "acceptAlways" {
						t.Fatalf("persistent grant advertised: %s", id)
					}
				}
				action := limitedServiceResponse(surface, policy, "ou_owner", record.RequestType, option)
				if commands := limitedServiceCommands(svc.ApplySurfaceAction(action)); len(commands) != 0 {
					t.Fatalf("persistent grant %q dispatched", option)
				}
			})
		}
	}
	// Native permission-profile changes are never granted through this entry;
	// owner can decline and then approve a separate native operation once.
	svc, surface, policy := limitedServiceRequest(t, "permission")
	record := pendingRequestRecord(surface, "limited-request")
	if commands := limitedServiceCommands(svc.ApplySurfaceAction(limitedServiceResponse(surface, policy, "ou_owner", record.RequestType, "accept"))); len(commands) != 0 {
		t.Fatal("permission mutation was accepted")
	}
}

func TestLimitedGroupsServiceSourceTopicsDoNotInheritSharedRoomWorkspace(t *testing.T) {
	svc := newRoomWorkspaceTestService(t)
	sharedSurface := gatewaypkg.SurfaceIDForInboundTopic("app-1", "oc_room", "group", "ou_owner", "")
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachWorkspace, SurfaceSessionID: sharedSurface, GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", WorkspaceKey: "/data/dl/droid"})
	room := svc.root.FeishuRoomContexts[feishuRoomContextID("oc_room")]
	if room == nil || room.WorkspaceKey != "/data/dl/droid" {
		t.Fatal("fixture did not establish shared room binding")
	}
	seen := map[string]bool{}
	for _, pair := range [][2]string{{"ou_pan", "om_topic_a"}, {"ou_other", "om_topic_a"}, {"ou_pan", "om_topic_b"}} {
		surfaceID := gatewaypkg.LimitedSurfaceID("app-1", "oc_room", pair[0], pair[1])
		policy := limitedServicePolicy(t)
		policy.Workspace = "/data/dl/droid"
		action := control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: surfaceID, GatewayID: "app-1", ChatID: "oc_room", ActorUserID: pair[0], Text: "read source", LimitedExecution: policy}
		_, waiting := svc.PrepareLimitedGroup(action)
		if !waiting {
			t.Fatal("private limited workspace was not prepared")
		}
		surface := svc.root.Surfaces[surfaceID]
		if surface == nil || surface.PendingHeadless == nil || surface.PendingHeadless.WorkspaceKey != policy.Workdir {
			t.Fatalf("source topic inherited a different workspace: %+v", surface)
		}
		if surface.AttachedInstanceID != "" || surfaceFeishuRoomID(surface) != "" {
			t.Fatal("limited topic attached to shared room workspace")
		}
		if seen[surface.PendingHeadless.WorkspaceKey] {
			t.Fatal("participants/topics share workdir")
		}
		seen[surface.PendingHeadless.WorkspaceKey] = true
	}
	if room.WorkspaceKey != "/data/dl/droid" {
		t.Fatal("limited topic overwrote ordinary room binding")
	}
}
