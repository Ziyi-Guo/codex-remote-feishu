package daemon

import (
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"testing"
)

func TestCronRequiresCollaboratorPermissionUpdates(t *testing.T) {
	requirements := feishuScopeRequirementsByFeature("cron_bitable")
	var grants []feishu.AppScopeStatus
	for _, r := range requirements {
		if r.Scope != "docs:permission.member:update" {
			grants = append(grants, feishu.AppScopeStatus{ScopeName: r.Scope, ScopeType: r.ScopeType, GrantStatus: 1})
		}
	}
	if decision := feishuScopePermissionDecisionFromScopes(requirements, grants, nil); decision.Allowed {
		t.Fatal("Cron allowed without permission to upgrade an existing collaborator to edit")
	}
	grants = append(grants, feishu.AppScopeStatus{ScopeName: "docs:permission.member:update", ScopeType: "tenant", GrantStatus: 1})
	if decision := feishuScopePermissionDecisionFromScopes(requirements, grants, nil); !decision.Allowed {
		t.Fatalf("all operation permissions denied: %#v", decision)
	}
}
