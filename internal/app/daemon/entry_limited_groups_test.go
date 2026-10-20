package daemon

import (
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func TestRuntimeGatewayAppsCarriesLimitedGroupPolicyAndStatePath(t *testing.T) {
	policy := limitedgroups.Policy{Enabled: true, ChatID: "oc_limited", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner"}, Workspace: "/workspace/limited", DailyLimit: 25}
	appConfig := config.AppConfig{Feishu: config.FeishuSettings{Apps: []config.FeishuAppConfig{{ID: "restricted", AppID: "cli_restricted", AppSecret: "secret", LimitedGroups: []limitedgroups.Policy{policy}}}}}
	paths := relayruntime.Paths{StateDir: t.TempDir(), DataDir: t.TempDir()}
	apps := runtimeGatewayApps(appConfig, config.ServicesConfig{}, paths)
	if len(apps) != 1 || len(apps[0].LimitedGroups) != 1 || !apps[0].LimitedGroups[0].Allows("oc_limited", "ou_owner") {
		t.Fatalf("lost policy: %#v", apps)
	}
	if apps[0].LimitedGroupsUsagePath != filepath.Join(paths.StateDir, "limited-groups-usage-restricted.json") {
		t.Fatalf("wrong usage path: %s", apps[0].LimitedGroupsUsagePath)
	}
	appConfig.Feishu.Apps[0].LimitedGroups[0].AllowedUserIDs[0] = "ou_changed"
	if apps[0].LimitedGroups[0].AllowedUserIDs[0] != "ou_owner" {
		t.Fatal("runtime policy shares config slice")
	}
}
