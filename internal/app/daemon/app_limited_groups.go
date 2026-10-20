package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func (a *App) limitedPolicy(gatewayID, chatID string) (limitedgroups.Policy, bool, error) {
	if a.admin.loadConfig == nil {
		return limitedgroups.Policy{}, false, nil
	}
	loaded, err := a.admin.loadConfig()
	if err != nil {
		return limitedgroups.Policy{}, false, err
	}
	for _, app := range loaded.Config.Feishu.Apps {
		if app.ID == gatewayID {
			p, ok := gatewaypkg.LimitedGroupPolicy(app.LimitedGroups, chatID)
			return p, ok, nil
		}
	}
	return limitedgroups.Policy{}, false, nil
}

func (a *App) guardLimitedInbound(action control.Action) (control.Action, bool) {
	ref, parsed := feishuidentity.ParseSurfaceRef(action.SurfaceSessionID)
	policy, limited, err := a.limitedPolicy(action.GatewayID, action.ChatID)
	if err != nil {
		return action, false
	}
	if !limited {
		if _, _, encoded := gatewaypkg.LimitedSurfaceIdentity(action.SurfaceSessionID); encoded {
			return action, false
		}
		return action, true
	}
	participant, _, ok := gatewaypkg.LimitedSurfaceIdentity(action.SurfaceSessionID)
	if !parsed || !ok || ref.ChatID() != action.ChatID || ref.GatewayID != action.GatewayID || !policy.Allows(action.ChatID, participant) || !policy.Allows(action.ChatID, action.ActorUserID) || !orchestrator.LimitedActionAllowed(action.Kind) {
		return action, false
	}
	if action.Request == nil && action.RequestControl == nil && participant != action.ActorUserID {
		return action, false
	}
	base := a.headlessRuntime.Paths.StateDir
	if base == "" {
		return action, false
	}
	hash := sha256.Sum256([]byte(action.SurfaceSessionID))
	dir := filepath.Join(base, "limited-groups", fmt.Sprintf("%x", hash[:16]))
	out := filepath.Join(dir, "output")
	if err := os.MkdirAll(out, 0700); err != nil {
		return action, false
	}
	// Never follow a model-created symlink into an unrelated writable directory.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return action, false
	}
	resolvedOut, err := filepath.EvalSymlinks(out)
	if err != nil || filepath.Dir(resolvedOut) != resolved {
		return action, false
	}
	p := &limitedgroups.ExecutionPolicy{OwnerUserID: policy.OwnerUserID, Workspace: policy.Workspace, Workdir: resolved, OutputDir: resolvedOut, Servers: policy.ExecutionServers, NativeConfigPath: policy.NativeConfigPath, NativeConfigSHA256: policy.NativeConfigSHA256}
	if p.Validate() != nil {
		return action, false
	}
	action.LimitedExecution = p
	return action, true
}

func (a *App) guardLimitedDispatch(surfaceID, instanceID string, command agentproto.Command) (agentproto.Command, error) {
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	if !ok {
		return command, nil
	}
	policy, limited, err := a.limitedPolicy(ref.GatewayID, ref.ChatID())
	if err != nil {
		return command, err
	}
	p := a.service.LimitedExecutionForSurface(surfaceID)
	participant, _, encoded := gatewaypkg.LimitedSurfaceIdentity(surfaceID)
	if !limited && p == nil && !encoded {
		return command, nil
	}
	if !encoded || p == nil || !policy.Allows(ref.ChatID(), participant) || p.Validate() != nil || p.OwnerUserID != policy.OwnerUserID || p.Workspace != policy.Workspace || !reflect.DeepEqual(p.Servers, policy.ExecutionServers) || p.NativeConfigPath != policy.NativeConfigPath || p.NativeConfigSHA256 != policy.NativeConfigSHA256 {
		return command, fmt.Errorf("limited group is disabled or policy changed")
	}
	if !a.service.InstanceSupportsLimitedGroups(instanceID, p.Workdir) {
		return command, fmt.Errorf("limited execution requires a private guarded wrapper")
	}
	command.Overrides.LimitedExecution = p
	if command.Kind == agentproto.CommandPromptSend {
		command.Target.CWD = p.Workdir
		command.Overrides.AccessMode = ""
	}
	return command, nil
}
