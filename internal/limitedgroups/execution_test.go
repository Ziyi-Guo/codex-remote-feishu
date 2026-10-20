package limitedgroups

import (
	"path/filepath"
	"testing"
)

func TestExecutionPolicyPreservesToolsAndRequiresHumanApproval(t *testing.T) {
	r := t.TempDir()
	p := ExecutionPolicy{OwnerUserID: "ou_owner", Workspace: filepath.Join(r, "project"), Workdir: filepath.Join(r, "delegate"), OutputDir: filepath.Join(r, "delegate", "output"), Servers: []ToolServer{{Name: "bytedcli", Tools: []string{"query", "write"}}, {Name: "cua_repl", PluginID: "unified-computer-use@openai-bundled", Tools: []string{"js"}}, {Name: "codex_apps", Connector: true}}}
	if p.Validate() != nil {
		t.Fatal("valid execution policy rejected")
	}
	c := p.ConfigOverrides()
	if c["approval_policy"] != "on-request" || c["approvals_reviewer"] != "user" || c["default_permissions"] != "profc_limited" {
		t.Fatal("approval must remain with a person")
	}
	if c[`mcp_servers.bytedcli.default_tools_approval_mode`] != "prompt" || c[`mcp_servers.bytedcli.tools.write.approval_mode`] != "prompt" {
		t.Fatal("MCP calls could inherit implicit approval")
	}
	if c[`plugins.unified-computer-use@openai-bundled.mcp_servers.cua_repl.tools.js.approval_mode`] != "prompt" || c["apps._default.default_tools_approval_mode"] != "prompt" {
		t.Fatal("plugin/app calls not guarded")
	}
	if _, ok := c["mcp_servers"]; ok {
		t.Fatal("must not replace existing connection or credentials")
	}
	fs := c["permissions.profc_limited.filesystem"].(map[string]any)
	if fs[p.Workspace] != "read" || fs[p.Workdir] != "read" || fs[p.OutputDir] != "write" || c["permissions.profc_limited.network.enabled"] != false {
		t.Fatal("sandbox boundary not preserved")
	}
	if _, ok := fs[filepath.Dir(r)]; ok {
		t.Fatal("parent data unnecessarily exposed")
	}
}

func TestExecutionPolicyRejectsWriteRootsAndUnknownAuthority(t *testing.T) {
	r := t.TempDir()
	base := ExecutionPolicy{OwnerUserID: "ou_owner", Workspace: filepath.Join(r, "project"), Workdir: filepath.Join(r, "delegate"), OutputDir: filepath.Join(r, "delegate", "output")}
	for name, change := range map[string]func(*ExecutionPolicy){
		"no owner":                  func(p *ExecutionPolicy) { p.OwnerUserID = "" },
		"root read":                 func(p *ExecutionPolicy) { p.Workspace = string(filepath.Separator) },
		"project output":            func(p *ExecutionPolicy) { p.OutputDir = p.Workspace },
		"outside output":            func(p *ExecutionPolicy) { p.OutputDir = filepath.Join(r, "other") },
		"workspace overlaps output": func(p *ExecutionPolicy) { p.Workspace = p.Workdir },
		"relative workdir":          func(p *ExecutionPolicy) { p.Workdir = "delegate" },
		"bad server":                func(p *ExecutionPolicy) { p.Servers = []ToolServer{{Name: "a\nb"}} },
	} {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			if p.Validate() == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
}
