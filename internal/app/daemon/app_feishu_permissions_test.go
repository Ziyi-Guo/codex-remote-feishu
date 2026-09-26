package daemon

import (
	"errors"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestApplyFeishuPermissionVerificationResultClearsGrantedGap(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
	if !app.observeFeishuPermissionError("app-1", &feishu.APIError{
		API:  "im.v1.message.create",
		Code: 99990001,
		Msg:  "permission denied",
		PermissionViolations: []feishu.APIErrorPermissionViolation{
			{Type: "tenant", Subject: "drive:drive"},
		},
	}) {
		t.Fatal("expected permission gap to be recorded")
	}

	app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{
		{ScopeName: "drive:drive", ScopeType: "tenant", GrantStatus: 1},
	}, nil)

	if got := app.snapshotFeishuPermissionGaps("app-1"); len(got) != 0 {
		t.Fatalf("expected granted scope to clear gap, got %#v", got)
	}
}

func TestApplyFeishuPermissionVerificationResultKeepsGapOnVerifyFailure(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
	if !app.observeFeishuPermissionError("app-1", &feishu.APIError{
		API:  "im.v1.message.create",
		Code: 99990001,
		Msg:  "permission denied",
		PermissionViolations: []feishu.APIErrorPermissionViolation{
			{Type: "tenant", Subject: "im:message"},
		},
	}) {
		t.Fatal("expected permission gap to be recorded")
	}

	app.applyFeishuPermissionVerificationResult("app-1", nil, errors.New("scope list failed"))

	got := app.snapshotFeishuPermissionGaps("app-1")
	if len(got) != 1 {
		t.Fatalf("expected verification failure to keep gap, got %#v", got)
	}
	if got[0].LastVerified.IsZero() {
		t.Fatalf("expected verify timestamp to be recorded, got %#v", got[0])
	}
}

func TestApplyFeishuPermissionVerificationResultClearsBrokerPermissionBlocks(t *testing.T) {
	gateway := &permissionClearingGateway{}
	app := New(":0", ":0", gateway, serverIdentityForTest())
	if !app.observeFeishuPermissionError("app-1", &feishu.APIError{
		API:  "im.v1.message.create",
		Code: 99990001,
		Msg:  "permission denied",
		PermissionViolations: []feishu.APIErrorPermissionViolation{
			{Type: "tenant", Subject: "im:message"},
		},
	}) {
		t.Fatal("expected permission gap to be recorded")
	}
	app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{
		{ScopeName: "im:message", ScopeType: "tenant", GrantStatus: 1},
	}, nil)
	if len(gateway.clearCalls) != 1 {
		t.Fatalf("expected permission block clear to be forwarded once, got %#v", gateway.clearCalls)
	}
	if gateway.clearCalls[0].gatewayID != "app-1" {
		t.Fatalf("unexpected gateway id: %#v", gateway.clearCalls[0])
	}
	if len(gateway.clearCalls[0].scopes) != 1 || gateway.clearCalls[0].scopes[0].ScopeName != "im:message" {
		t.Fatalf("unexpected forwarded scopes: %#v", gateway.clearCalls[0].scopes)
	}
}

func TestPrimaryPermissionDecisionAcceptsGroupMessageScopes(t *testing.T) {
	for _, scope := range []string{"im:message.group_msg", "im:message.group_msg:readonly"} {
		decision := primaryPermissionDecisionFromScopes([]feishu.AppScopeStatus{
			{ScopeName: scope, ScopeType: "tenant", GrantStatus: 1},
		}, nil)
		if !decision.Allowed || decision.Scope != scope {
			t.Fatalf("scope %s decision = %#v, want allowed", scope, decision)
		}
	}
}

func TestPrimaryPermissionDecisionRejectsUserGroupMessageScope(t *testing.T) {
	decision := primaryPermissionDecisionFromScopes([]feishu.AppScopeStatus{
		{ScopeName: "im:message.group_msg", ScopeType: "user", GrantStatus: 1},
	}, nil)
	if decision.Allowed || decision.Reason != "missing_group_message_scope" {
		t.Fatalf("user-token scope decision = %#v, want missing tenant scope", decision)
	}
}

func TestPrimaryPermissionDecisionRejectsMissingScopeAndErrors(t *testing.T) {
	if decision := primaryPermissionDecisionFromScopes([]feishu.AppScopeStatus{
		{ScopeName: "im:message", ScopeType: "tenant", GrantStatus: 1},
	}, nil); decision.Allowed || decision.Reason != "missing_group_message_scope" {
		t.Fatalf("missing scope decision = %#v, want missing", decision)
	}
	if decision := primaryPermissionDecisionFromScopes(nil, errors.New("boom")); decision.Allowed || decision.Reason != "scope_read_failed" || decision.Err == nil {
		t.Fatalf("error decision = %#v, want failed with err", decision)
	}
}

func serverIdentityForTest() agentproto.ServerIdentity {
	return agentproto.ServerIdentity{
		PID:       42,
		StartedAt: time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC),
	}
}

type permissionClearingGateway struct {
	recordingGateway
	clearCalls []permissionClearCall
}

type permissionClearCall struct {
	gatewayID string
	scopes    []feishu.AppScopeStatus
}

func (g *permissionClearingGateway) ClearGrantedPermissionBlocks(gatewayID string, scopes []feishu.AppScopeStatus) {
	g.clearCalls = append(g.clearCalls, permissionClearCall{
		gatewayID: gatewayID,
		scopes:    append([]feishu.AppScopeStatus(nil), scopes...),
	})
}

func TestPermissionVerificationDoesNotCrossIdentities(t *testing.T) {
	for _, identity := range []string{"tenant", ""} {
		app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
		app.observeFeishuPermissionError("app-1", &feishu.APIError{PermissionViolations: []feishu.APIErrorPermissionViolation{{Type: identity, Subject: "drive:drive"}}})
		app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{{ScopeName: "drive:drive", ScopeType: "user", GrantStatus: 1}}, nil)
		if got := app.snapshotFeishuPermissionGaps("app-1"); len(got) != 1 {
			t.Fatalf("%q gap cleared by user grant: %#v", identity, got)
		}
	}
}

func TestPermissionVerificationKeepsDifferentAPIRequirements(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
	app.observeFeishuPermissionError("app-1", &feishu.APIError{API: "drive.v1.file.list", Msg: "One of the following scopes is required: [drive:drive, drive:drive:readonly]", PermissionViolations: []feishu.APIErrorPermissionViolation{{Type: "tenant", Subject: "drive:drive"}}})
	app.observeFeishuPermissionError("app-1", &feishu.APIError{API: "drive.v1.file.upload_all", PermissionViolations: []feishu.APIErrorPermissionViolation{{Type: "tenant", Subject: "drive:drive"}}})
	if got := app.snapshotFeishuPermissionGaps("app-1"); len(got) != 2 {
		t.Fatalf("requirements conflated: %#v", got)
	}
	app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 1}}, nil)
	if got := app.snapshotFeishuPermissionGaps("app-1"); len(got) != 1 || got[0].SourceAPI != "drive.v1.file.upload_all" {
		t.Fatalf("write gap cleared: %#v", got)
	}
}

func TestPermissionVerificationForwardsGrantsWithoutUIGaps(t *testing.T) {
	gateway := &permissionClearingGateway{}
	app := New(":0", ":0", gateway, serverIdentityForTest())
	app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{{ScopeName: "drive:drive", ScopeType: "tenant", GrantStatus: 1}}, nil)
	if len(gateway.clearCalls) != 1 {
		t.Fatal("broker refresh skipped when UI has no gap")
	}
}

func TestFeaturePermissionRequiresEveryOperation(t *testing.T) {
	requirements := feishuScopeRequirementsByFeature("cron_bitable")
	if len(requirements) < 2 {
		t.Fatalf("Cron requires multiple API operations; got %#v", requirements)
	}
	partial := []feishu.AppScopeStatus{
		{ScopeName: "base:app:create", ScopeType: "tenant", GrantStatus: 1},
		{ScopeName: "base:table:read", ScopeType: "tenant", GrantStatus: 1},
	}
	if decision := feishuScopePermissionDecisionFromScopes(requirements, partial, nil); decision.Allowed {
		t.Fatalf("partial operations allowed Cron: %#v", decision)
	}
	broad := []feishu.AppScopeStatus{{ScopeName: "bitable:app", ScopeType: "tenant", GrantStatus: 1}}
	if decision := feishuScopePermissionDecisionFromScopes(requirements, broad, nil); !decision.Allowed {
		t.Fatalf("legacy broad grant denied: %#v", decision)
	}
}

func TestFactsRefreshAlwaysVerifiesBrokerWithoutUIGap(t *testing.T) {
	gateway := &permissionClearingGateway{}
	app := New(":0", ":0", gateway, serverIdentityForTest())
	scopes := []feishu.AppScopeStatus{{ScopeName: "drive:file:upload", ScopeType: "tenant", GrantStatus: 1}}
	app.afterFeishuFactsRefresh("app-1", scopes, nil)
	if len(gateway.clearCalls) != 1 {
		t.Fatal("facts refresh skipped broker without UI gap")
	}
	app.afterFeishuFactsRefresh("app-1", scopes, errors.New("grant read failed"))
	if len(gateway.clearCalls) != 1 {
		t.Fatal("failed refresh cleared broker")
	}
}

func TestMixedIndependentPermissionsStayVisibleAfterPartialGrant(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, serverIdentityForTest())
	app.observeFeishuPermissionError("app-1", &feishu.APIError{PermissionViolations: []feishu.APIErrorPermissionViolation{
		{Type: "tenant", Subject: "drive:file:upload"}, {Type: "user", Subject: "base:record:update"},
	}})
	app.applyFeishuPermissionVerificationResult("app-1", []feishu.AppScopeStatus{{ScopeName: "drive:file:upload", ScopeType: "tenant", GrantStatus: 1}}, nil)
	if got := app.snapshotFeishuPermissionGaps("app-1"); len(got) != 1 || len(got[0].UnresolvedPermissions) != 2 {
		t.Fatalf("partial grant hid other identity: %#v", got)
	}
}
