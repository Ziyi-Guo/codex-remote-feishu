package limitedgroups

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var configIdentifier = regexp.MustCompile(`^[A-Za-z0-9_@-]+$`)

type ToolServer struct {
	Name      string   `json:"name"`
	PluginID  string   `json:"pluginId,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	Connector bool     `json:"connector,omitempty"`
}

// ExecutionPolicy is daemon-authored, never populated from model or chat input.
type ExecutionPolicy struct {
	OwnerUserID        string       `json:"ownerUserId"`
	Workspace          string       `json:"workspace"`
	Workdir            string       `json:"workdir"`
	OutputDir          string       `json:"outputDir"`
	Servers            []ToolServer `json:"servers,omitempty"`
	NativeConfigPath   string       `json:"nativeConfigPath,omitempty"`
	NativeConfigSHA256 string       `json:"nativeConfigSha256,omitempty"`
}

func (p ExecutionPolicy) Validate() error {
	if p.NativeConfigPath != "" || p.NativeConfigSHA256 != "" {
		data, err := os.ReadFile(p.NativeConfigPath)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != p.NativeConfigSHA256 {
			return fmt.Errorf("native tool configuration changed; limited execution must be revalidated")
		}
	}
	if !validPrincipal(p.OwnerUserID, "ou_") {
		return fmt.Errorf("limited execution requires an owner")
	}
	for _, v := range []string{p.Workspace, p.Workdir, p.OutputDir} {
		if !filepath.IsAbs(v) || filepath.Clean(v) != v || v == string(filepath.Separator) {
			return fmt.Errorf("invalid limited execution root")
		}
	}
	if !within(p.Workdir, p.OutputDir) || p.Workdir == p.OutputDir || within(p.Workspace, p.Workdir) || within(p.Workdir, p.Workspace) {
		return fmt.Errorf("limited execution roots overlap")
	}
	for _, s := range p.Servers {
		for _, v := range append([]string{s.Name, s.PluginID}, s.Tools...) {
			if v != "" && !configIdentifier.MatchString(v) {
				return fmt.Errorf("invalid tool policy name")
			}
		}
		if s.Name == "" {
			return fmt.Errorf("missing tool server")
		}
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (p ExecutionPolicy) ConfigOverrides() map[string]any {
	c := map[string]any{"approval_policy": "on-request", "approvals_reviewer": "user", "default_permissions": "profc_limited",
		"permissions.profc_limited.filesystem":      map[string]any{":minimal": "read", p.Workspace: "read", p.Workdir: "read", p.OutputDir: "write"},
		"permissions.profc_limited.network.enabled": false, "apps._default.default_tools_approval_mode": "prompt"}
	// Automatic hooks, personal memory and child agents are outside this wrapper's
	// per-request confirmation and conversation ownership boundary.
	for _, feature := range []string{"hooks", "memories", "chronicle", "multi_agent", "multi_agent_v2", "goals", "shell_snapshot", "skill_mcp_dependency_install"} {
		c["features."+feature] = false
	}
	c["features.write_stdin_approval"] = true
	for _, s := range p.Servers {
		if s.Connector {
			continue
		}
		// App-server config keys split on dots; TOML quotes become literal names.
		prefix := "mcp_servers." + s.Name
		if s.PluginID != "" {
			prefix = "plugins." + s.PluginID + "." + prefix
		}
		c[prefix+".default_tools_approval_mode"] = "prompt"
		for _, tool := range s.Tools {
			c[prefix+".tools."+tool+".approval_mode"] = "prompt"
		}
	}
	return c
}
