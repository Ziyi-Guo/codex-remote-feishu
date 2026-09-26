package feishu

import (
	previewpkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/preview"
	"net/http"
	"testing"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestExtractPermissionGapFromAPIError(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{
		API:  "im.v1.message.create",
		Code: 99990001,
		Msg:  "permission denied",
		PermissionViolations: []APIErrorPermissionViolation{
			{Type: "tenant", Subject: "drive:drive"},
		},
		Helps: []APIErrorHelp{
			{URL: "https://open.feishu.cn/permission/apply"},
		},
		RequestID: "req-1",
	})
	if !ok {
		t.Fatal("expected permission gap to be extracted")
	}
	if gap.Scope != "drive:drive" || gap.ScopeType != "tenant" {
		t.Fatalf("unexpected gap scope: %#v", gap)
	}
	if gap.ApplyURL != "https://open.feishu.cn/permission/apply" {
		t.Fatalf("unexpected apply url: %#v", gap)
	}
	if gap.SourceAPI != "im.v1.message.create" || gap.RequestID != "req-1" {
		t.Fatalf("unexpected gap metadata: %#v", gap)
	}
}

func TestExtractPermissionGapFromDriveAPIError(t *testing.T) {
	gap, ok := ExtractPermissionGap(&previewpkg.DriveAPIError{
		API:       "drive.v1.file.upload_all",
		Code:      99991672,
		Msg:       driveAnyOfError,
		RequestID: "req-drive-1",
	})
	if !ok {
		t.Fatal("expected drive permission gap to be extracted")
	}
	if gap.Scope != "drive:drive" || gap.ScopeType != "tenant" {
		t.Fatalf("unexpected drive gap: %#v", gap)
	}
	if gap.SourceAPI != "drive.v1.file.upload_all" || gap.RequestID != "req-drive-1" {
		t.Fatalf("unexpected drive gap metadata: %#v", gap)
	}
}

func TestExtractRateLimitFromAPIError(t *testing.T) {
	resp := &larkcore.ApiResp{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"x-ogw-ratelimit-reset": []string{"0.5"},
			"Retry-After":           []string{"1"},
			larkcore.HttpHeaderKeyRequestId: []string{
				"req-rate-1",
			},
		},
	}
	err := newAPIError("im.v1.message.create", resp, larkcore.CodeError{
		Code: 99991400,
		Msg:  "rate limited",
	})
	rate, ok := ExtractRateLimit(err)
	if !ok {
		t.Fatal("expected rate-limit evidence to be extracted")
	}
	if rate.API != "im.v1.message.create" || rate.RequestID != "req-rate-1" {
		t.Fatalf("unexpected rate-limit metadata: %#v", rate)
	}
	if rate.StatusCode != http.StatusTooManyRequests || rate.ErrorCode != 99991400 {
		t.Fatalf("unexpected rate-limit codes: %#v", rate)
	}
	if rate.RateLimitResetAfter < 450*time.Millisecond || rate.RateLimitResetAfter > 550*time.Millisecond {
		t.Fatalf("unexpected reset duration: %s", rate.RateLimitResetAfter)
	}
	if rate.RetryAfter < 950*time.Millisecond || rate.RetryAfter > 1050*time.Millisecond {
		t.Fatalf("unexpected retry-after duration: %s", rate.RetryAfter)
	}
}

const driveAnyOfError = "Access denied. One of the following scopes is required: [drive:drive, drive:drive:readonly, space:document:retrieve].应用尚未开通所需的应用身份权限：[drive:drive,drive:drive:readonly,space:document:retrieve]，点击链接申请并开通任一权限即可：https://open.feishu.cn/app/cli_demo/auth?q=drive:drive,drive:drive:readonly,space:document:retrieve&op_from=openapi&token_type=tenant"

func TestPermissionErrorRequiresExplicitScopeEvidence(t *testing.T) {
	for _, err := range []error{
		&previewpkg.DriveAPIError{Code: 99991672, Msg: "Access denied"},
		&previewpkg.DriveAPIError{Code: 1061004, Msg: "file not visible to caller"},
		&APIError{Code: 99991672, Msg: "file ACL denied; configured drive:drive"},
	} {
		if gap, ok := ExtractPermissionGap(err); ok {
			t.Errorf("invented scope gap: %#v", gap)
		}
	}
}

func TestPermissionAnyOfErrorPreservesIdentityAndRecovery(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{API: "drive.v1.file.list", Code: 99991672, Msg: driveAnyOfError})
	if !ok || gap.ScopeType != "tenant" || gap.ApplyURL == "" {
		t.Fatalf("lost official evidence: %#v", gap)
	}
	broker := NewFeishuCallBroker("app-1", nil)
	spec := CallSpec{API: "drive.v1.file.list"}
	broker.markPermissionBlocked(spec, gap)
	broker.ClearGrantedPermissionBlocks([]AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "user", GrantStatus: 1}})
	if blocked, _ := broker.currentPermissionBlock(spec); blocked == nil {
		t.Fatal("user grant cleared tenant gap")
	}
	broker.ClearGrantedPermissionBlocks([]AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 2}})
	if blocked, _ := broker.currentPermissionBlock(spec); blocked == nil {
		t.Fatal("pending grant cleared gap")
	}
	broker.ClearGrantedPermissionBlocks([]AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 1}})
	if blocked, _ := broker.currentPermissionBlock(spec); blocked != nil {
		t.Fatal("any-of grant did not clear gap")
	}
}

func TestPermissionGapExplicitAlternatives(t *testing.T) {
	cases := []struct {
		name     string
		err      *APIError
		identity string
	}{
		{"message", &APIError{Msg: driveAnyOfError}, "tenant"},
		{"apply_link_over_help", &APIError{Msg: driveAnyOfError, Helps: []APIErrorHelp{{URL: "https://open.feishu.cn/document/help"}}}, "tenant"},
		{"structured_description", &APIError{PermissionViolations: []APIErrorPermissionViolation{{Type: "tenant", Subject: "drive:drive", Description: "One of the following scopes is required: [drive:drive, drive:drive:readonly, space:document:retrieve]"}}}, "tenant"},
		{"chinese", &APIError{Msg: "应用尚未开通所需的用户身份权限：[drive:drive,drive:drive:readonly]，点击链接申请并开通任一权限即可：https://open.feishu.cn/app/cli_demo/auth?q=drive:drive&token_type=user"}, "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gap, ok := ExtractPermissionGap(tc.err)
			if !ok || len(gap.Scopes) < 2 || gap.Scopes[1] != "drive:drive:readonly" || gap.ScopeType != tc.identity {
				t.Fatalf("gap=%#v ok=%t", gap, ok)
			}
		})
	}
}

func TestPermissionGapSatisfiedUsesSharedSatisfiersAndKnownIdentity(t *testing.T) {
	grants := []AppScopeStatus{{ScopeName: "im:chat", ScopeType: "tenant", GrantStatus: 1}}
	if !PermissionGapSatisfied(PermissionGapEvidence{Scope: "im:chat:readonly", ScopeType: "tenant"}, grants) {
		t.Fatal("shared satisfier was ignored")
	}
	for _, identity := range []string{"", "unknown", "user"} {
		if PermissionGapSatisfied(PermissionGapEvidence{Scope: "im:chat:readonly", ScopeType: identity}, grants) {
			t.Fatalf("identity %q was cleared", identity)
		}
	}
}

func TestPermissionErrorExplicitSingleScopeWithoutIdentity(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{API: "application.get", Code: 99991663, StatusCode: 400, Msg: "missing application:application:self_manage"})
	if !ok || gap.Scope != "application:application:self_manage" || gap.ScopeType != "" {
		t.Fatalf("explicit missing scope lost: %#v %v", gap, ok)
	}
	if PermissionGapSatisfied(gap, []AppScopeStatus{{ScopeName: gap.Scope, ScopeType: "tenant", GrantStatus: 1}}) {
		t.Fatal("unknown identity cleared")
	}
	for _, msg := range []string{"file missing; configured application:application:self_manage", "missing file ACL despite drive:drive", "permission denied while requesting drive:drive"} {
		if gap, ok := ExtractPermissionGap(&APIError{Msg: msg}); ok {
			t.Fatalf("unrelated scope mention extracted: %#v", gap)
		}
	}
}

func TestIndependentPermissionViolationsCannotClearByFirstGrant(t *testing.T) {
	for _, identity := range []string{"user", "tenant"} {
		gap, ok := ExtractPermissionGap(&APIError{PermissionViolations: []APIErrorPermissionViolation{
			{Type: "tenant", Subject: "drive:file:upload"},
			{Type: identity, Subject: "base:record:update"},
		}})
		if !ok {
			t.Fatal("lost independent violations")
		}
		if PermissionGapSatisfied(gap, []AppScopeStatus{{ScopeName: "drive:file:upload", ScopeType: "tenant", GrantStatus: 1}}) {
			t.Fatalf("partial grant cleared %q violations: %#v", identity, gap)
		}
	}
}

func TestExplicitAnyOfCoversSameIdentityStructuredViolations(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{Msg: driveAnyOfError, PermissionViolations: []APIErrorPermissionViolation{
		{Type: "tenant", Subject: "drive:drive"},
		{Type: "tenant", Subject: "drive:drive:readonly"},
	}})
	if !ok || !PermissionGapSatisfied(gap, []AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 1}}) {
		t.Fatalf("explicit same-identity any-of rejected: %#v", gap)
	}
}

func TestExplicitAnyOfDoesNotHideIndependentViolation(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{Msg: driveAnyOfError, PermissionViolations: []APIErrorPermissionViolation{{Type: "tenant", Subject: "base:record:update"}}})
	if !ok || len(gap.UnresolvedPermissions) != 2 {
		t.Fatalf("independent requirement lost: %#v", gap)
	}
	if PermissionGapSatisfied(gap, []AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 1}}) {
		t.Fatal("any-of hid independent violation")
	}
}

func TestExplicitAnyOfRequestIdentityCoversUntypedViolations(t *testing.T) {
	gap, ok := ExtractPermissionGap(&APIError{Msg: driveAnyOfError, PermissionViolations: []APIErrorPermissionViolation{
		{Type: "action_scope", Subject: "drive:drive"}, {Type: "action_scope", Subject: "drive:drive:readonly"},
	}})
	if !ok || !PermissionGapSatisfied(gap, []AppScopeStatus{{ScopeName: "drive:drive:readonly", ScopeType: "tenant", GrantStatus: 1}}) {
		t.Fatalf("known request identity lost: %#v", gap)
	}
}
