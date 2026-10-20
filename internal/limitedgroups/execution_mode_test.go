package limitedgroups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyNetworkNeverPolicy(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureForbiddenRules(work); err != nil {
		t.Fatal(err)
	}
	hash, err := ForbiddenRulesSHA256(work)
	if err != nil {
		t.Fatal(err)
	}
	p := ExecutionPolicy{ExecutionMode: ReadOnlyNetworkNever, RulesSHA256: hash, OwnerUserID: "ou_owner", Workspace: filepath.Join(root, "source"), Workdir: work, OutputDir: filepath.Join(work, "output"), Servers: []ToolServer{{Name: "business", Tools: []string{"query"}}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	c := p.ConfigOverrides()
	if c["approval_policy"] != "never" || c["permissions.profc_limited.network.enabled"] != true || c["apps._default.default_tools_approval_mode"] != "approve" || c["mcp_servers.business.tools.query.approval_mode"] != "approve" {
		t.Fatalf("wrong mode: %#v", c)
	}
	fs := c["permissions.profc_limited.filesystem"].(map[string]any)
	for _, v := range fs {
		if v != "read" {
			t.Fatalf("writable filesystem: %#v", fs)
		}
	}
	if fs[string(filepath.Separator)] != "read" {
		t.Fatal("approved read-only filesystem missing")
	}
	if err := os.WriteFile(filepath.Join(work, ".codex", "rules", forbiddenRulesFilename), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.Validate() == nil {
		t.Fatal("changed rules accepted")
	}
}
