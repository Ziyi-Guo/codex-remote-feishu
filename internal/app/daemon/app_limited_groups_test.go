package daemon

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func limitedDaemonFixture(t *testing.T) (*App, *config.LoadedAppConfig, control.Action) {
	t.Helper()
	app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
	app.headlessRuntime.Paths.StateDir = t.TempDir()
	policy := limitedgroups.Policy{Enabled: true, ChatID: "oc_limited", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner", "ou_pan", "ou_other"}, Workspace: t.TempDir(), DailyLimit: 25}
	loaded := &config.LoadedAppConfig{Config: config.AppConfig{Feishu: config.FeishuSettings{Apps: []config.FeishuAppConfig{{ID: "app-limited", LimitedGroups: []limitedgroups.Policy{policy}}}}}}
	app.admin.loadConfig = func() (config.LoadedAppConfig, error) { return *loaded, nil }
	action := control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: gatewaypkg.LimitedSurfaceID("app-limited", "oc_limited", "ou_pan", "om_source"), GatewayID: "app-limited", ChatID: "oc_limited", ActorUserID: "ou_pan", Text: "query source"}
	return app, loaded, action
}

func bindLimitedDaemonWrapper(t *testing.T, app *App, action control.Action) control.Action {
	t.Helper()
	guarded, ok := app.guardLimitedInbound(action)
	if !ok || guarded.LimitedExecution == nil {
		t.Fatal("valid limited ingress rejected")
	}
	app.service.MaterializeSurface(guarded.SurfaceSessionID, guarded.GatewayID, guarded.ChatID, guarded.ActorUserID)
	app.service.Surface(guarded.SurfaceSessionID).LimitedExecution = guarded.LimitedExecution
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "limited-wrapper", WorkspaceRoot: guarded.LimitedExecution.Workdir, WorkspaceKey: guarded.LimitedExecution.Workdir, Backend: agentproto.BackendCodex, Managed: true, Online: true, Capabilities: agentproto.Capabilities{LimitedGroups: true}})
	return guarded
}

func TestLimitedGroupsDaemonInboundFailsClosed(t *testing.T) {
	for _, scenario := range []string{"disabled", "invalid policy", "duplicate policies", "not allowlisted actor", "not allowlisted participant", "personal surface", "different actor", "different chat", "different gateway", "reserved surface after removal", "config error", "missing config loader"} {
		t.Run(scenario, func(t *testing.T) {
			app, loaded, action := limitedDaemonFixture(t)
			policy := &loaded.Config.Feishu.Apps[0].LimitedGroups[0]
			switch scenario {
			case "disabled":
				policy.Enabled = false
			case "invalid policy":
				policy.DailyLimit = 0
			case "duplicate policies":
				loaded.Config.Feishu.Apps[0].LimitedGroups = append(loaded.Config.Feishu.Apps[0].LimitedGroups, *policy)
			case "not allowlisted actor":
				action.ActorUserID = "ou_outsider"
			case "not allowlisted participant":
				action.SurfaceSessionID = gatewaypkg.LimitedSurfaceID(action.GatewayID, action.ChatID, "ou_outsider", "om_source")
				action.ActorUserID = "ou_owner"
				action.Request = &control.ActionRequestResponse{RequestID: "req"}
			case "personal surface":
				action.SurfaceSessionID = gatewaypkg.SurfaceIDForInboundTopic(action.GatewayID, action.ChatID, "p2p", action.ActorUserID, "")
			case "different actor":
				action.ActorUserID = "ou_other"
			case "different chat":
				action.ChatID = "oc_other"
			case "different gateway":
				action.GatewayID = "other-app"
			case "reserved surface after removal":
				loaded.Config.Feishu.Apps[0].LimitedGroups = nil
			case "config error":
				app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
					return config.LoadedAppConfig{}, errors.New("config unavailable")
				}
			case "missing config loader":
				app.admin.loadConfig = nil
			}
			if _, ok := app.guardLimitedInbound(action); ok {
				t.Fatalf("unsafe limited ingress %q accepted", scenario)
			}
		})
	}
}

func TestLimitedGroupsDaemonOwnerCallbackCanTargetPanSurface(t *testing.T) {
	app, _, action := limitedDaemonFixture(t)
	original, ok := app.guardLimitedInbound(action)
	if !ok {
		t.Fatal("participant ingress rejected")
	}
	action.Kind = control.ActionRespondRequest
	action.ActorUserID = "ou_owner"
	action.Request = &control.ActionRequestResponse{RequestID: "req-native", RequestType: "approval", RequestOptionID: "accept"}
	callback, ok := app.guardLimitedInbound(action)
	if !ok || callback.ActorUserID != "ou_owner" || callback.SurfaceSessionID != original.SurfaceSessionID || !reflect.DeepEqual(callback.LimitedExecution, original.LimitedExecution) {
		t.Fatal("verified owner could not review Pan's exact source-topic request")
	}
}

func TestLimitedGroupsDaemonSourceTopicWorkdirsArePrivate(t *testing.T) {
	app, _, action := limitedDaemonFixture(t)
	seen := map[string]bool{}
	for _, pair := range [][2]string{{"ou_pan", "om_source"}, {"ou_other", "om_source"}, {"ou_pan", "om_second"}} {
		action.SurfaceSessionID = gatewaypkg.LimitedSurfaceID(action.GatewayID, action.ChatID, pair[0], pair[1])
		action.ActorUserID = pair[0]
		guarded, ok := app.guardLimitedInbound(action)
		if !ok {
			t.Fatal("valid participant/topic rejected")
		}
		p := guarded.LimitedExecution
		if p == nil || seen[p.Workdir] || p.OutputDir != filepath.Join(p.Workdir, "output") || p.Workdir == p.Workspace {
			t.Fatalf("source-topic private workspace invariant failed: %+v", p)
		}
		seen[p.Workdir] = true
	}
}

func TestLimitedGroupsDaemonDispatchRequiresGuardedPrivateWrapper(t *testing.T) {
	for _, scenario := range []string{"valid", "old wrapper", "unmanaged wrapper", "other workspace", "other backend"} {
		t.Run(scenario, func(t *testing.T) {
			app, _, action := limitedDaemonFixture(t)
			guarded := bindLimitedDaemonWrapper(t, app, action)
			instance := state.InstanceRecord{InstanceID: "limited-wrapper", WorkspaceRoot: guarded.LimitedExecution.Workdir, WorkspaceKey: guarded.LimitedExecution.Workdir, Backend: agentproto.BackendCodex, Managed: true, Online: true, Capabilities: agentproto.Capabilities{LimitedGroups: true}}
			switch scenario {
			case "old wrapper":
				instance.Capabilities.LimitedGroups = false
			case "unmanaged wrapper":
				instance.Managed = false
			case "other workspace":
				instance.WorkspaceRoot = t.TempDir()
			case "other backend":
				instance.Backend = agentproto.BackendClaude
			}
			app.service.UpsertInstance(&instance)
			command := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{CWD: t.TempDir()}, Overrides: agentproto.PromptOverrides{AccessMode: agentproto.AccessModeFullAccess}}
			got, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, instance.InstanceID, command)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("unsupported wrapper accepted limited dispatch")
				}
				return
			}
			if err != nil || got.Overrides.LimitedExecution == nil || got.Target.CWD != guarded.LimitedExecution.Workdir || got.Overrides.AccessMode != "" {
				t.Fatalf("limited dispatch did not pin workdir/guard: %+v err=%v", got, err)
			}
		})
	}
}

func TestLimitedGroupsDaemonPolicyOffBlocksPendingDispatch(t *testing.T) {
	for _, scenario := range []string{"disabled", "removed", "invalid", "participant removed", "workspace changed", "owner changed", "config unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			app, loaded, action := limitedDaemonFixture(t)
			guarded := bindLimitedDaemonWrapper(t, app, action)
			command := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{CWD: guarded.LimitedExecution.Workdir}}
			if _, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, "limited-wrapper", command); err != nil {
				t.Fatalf("valid pending dispatch rejected: %v", err)
			}
			policy := &loaded.Config.Feishu.Apps[0].LimitedGroups[0]
			switch scenario {
			case "disabled":
				policy.Enabled = false
			case "removed":
				loaded.Config.Feishu.Apps[0].LimitedGroups = nil
			case "invalid":
				policy.DailyLimit = 0
			case "participant removed":
				policy.AllowedUserIDs = []string{"ou_owner"}
			case "workspace changed":
				policy.Workspace = t.TempDir()
			case "owner changed":
				policy.OwnerUserID = "ou_other"
			case "config unavailable":
				app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
					return config.LoadedAppConfig{}, errors.New("config unavailable")
				}
			}
			if _, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, "limited-wrapper", command); err == nil {
				t.Fatalf("pending dispatch survived policy %s", scenario)
			}
		})
	}
}

func TestLimitedGroupsDaemonRemovedPolicyCannotReclassifyOrphanAsPersonal(t *testing.T) {
	app, loaded, action := limitedDaemonFixture(t)
	// A reserved encoded surface is never an ordinary personal surface, even if
	// its persisted surface guard or the complete policy entry has disappeared.
	loaded.Config.Feishu.Apps[0].LimitedGroups = nil
	command := agentproto.Command{Kind: agentproto.CommandPromptSend, Overrides: agentproto.PromptOverrides{AccessMode: agentproto.AccessModeFullAccess}}
	if _, err := app.guardLimitedDispatch(action.SurfaceSessionID, "personal-wrapper", command); err == nil {
		t.Fatal("removed limited surface fell through to unguarded personal dispatch")
	}
}

func TestLimitedGroupsDaemonOrdinaryPersonalSessionsAreUnchanged(t *testing.T) {
	app, _, action := limitedDaemonFixture(t)
	action.ChatID = "oc_personal"
	action.ActorUserID = "ou_owner"
	action.SurfaceSessionID = gatewaypkg.SurfaceIDForInboundTopic(action.GatewayID, action.ChatID, "p2p", action.ActorUserID, "")
	original := action
	got, ok := app.guardLimitedInbound(action)
	if !ok || !reflect.DeepEqual(got, original) {
		t.Fatal("unrelated personal ingress changed")
	}
	command := agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{CWD: t.TempDir()}, Overrides: agentproto.PromptOverrides{AccessMode: agentproto.AccessModeFullAccess}}
	dispatched, err := app.guardLimitedDispatch(action.SurfaceSessionID, "personal-wrapper", command)
	if err != nil || !reflect.DeepEqual(dispatched, command) {
		t.Fatal("unrelated personal dispatch changed")
	}
}
