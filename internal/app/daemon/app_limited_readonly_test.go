package daemon

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func TestReadOnlyModeRequiresPrivateRulesAndUpdatedWrapper(t *testing.T) {
	app, loaded, action := limitedDaemonFixture(t)
	loaded.Config.Feishu.Apps[0].LimitedGroups[0].ExecutionMode = limitedgroups.ReadOnlyNetworkNever
	guarded := bindLimitedDaemonWrapper(t, app, action)
	if !guarded.LimitedExecution.ReadOnlyNetworkNever() || guarded.LimitedExecution.RulesSHA256 == "" {
		t.Fatal("new mode/rules lost")
	}
	command := agentproto.Command{Kind: agentproto.CommandPromptSend}
	if _, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, "limited-wrapper", command); err == nil {
		t.Fatal("old wrapper accepted")
	}
	inst := app.service.Instance("limited-wrapper")
	inst.Capabilities.LimitedReadOnlyNetwork = true
	if _, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, "limited-wrapper", command); err != nil {
		t.Fatal(err)
	}
	loaded.Config.Feishu.Apps[0].LimitedGroups[0].ExecutionMode = ""
	if _, err := app.guardLimitedDispatch(guarded.SurfaceSessionID, "limited-wrapper", command); err == nil {
		t.Fatal("changed mode accepted")
	}
}
