package feishu

import (
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"strings"
	"testing"
)

func TestPermissionGapProjectionShowsAnyOfAndUnknownIdentity(t *testing.T) {
	lines := formatSnapshotPermissionGapsPlain([]control.PermissionGapSummary{
		{Scope: "drive:drive", Scopes: []string{"drive:drive", "drive:drive:readonly"}, ScopeType: "tenant", SourceAPI: "drive.v1.file.list"},
		{Scope: "im:chat:readonly"},
	})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"任选一项：drive:drive / drive:drive:readonly", "应用身份", "drive.v1.file.list", "身份待确认，授权后需重试验证"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestPermissionGapProjectionPreservesIndependentRequirements(t *testing.T) {
	lines := formatSnapshotPermissionGapsPlain([]control.PermissionGapSummary{{Scope: "drive:file:upload", ScopeType: "tenant", UnresolvedPermissions: []string{"tenant: drive:file:upload", "user: base:record:update"}}})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"组合关系未明确", "tenant: drive:file:upload", "user: base:record:update"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "任选") || strings.Contains(text, " · 应用身份") {
		t.Fatalf("invented combined semantics: %s", text)
	}
}
