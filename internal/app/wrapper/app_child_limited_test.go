package wrapper

import (
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func TestPrivateNativeRulesTrustOnlyLimitedMode(t *testing.T) {
	base := []string{"app-server", "--stdio"}
	for _, mode := range []string{"", limitedgroups.ReadOnlyNetworkNever} {
		t.Setenv(limitedgroups.ProcessEnv, "1")
		t.Setenv(limitedgroups.ExecutionModeEnv, mode)
		app := New(Config{Source: "vscode", WorkspaceRoot: "/private/query cwd"})
		args, _ := app.buildCodexChildLaunch(base)
		if mode == "" && !reflect.DeepEqual(args, base) {
			t.Fatal("legacy launch changed")
		}
		if mode != "" && args[len(args)-1] != `projects={"/private/query cwd"={trust_level="trusted"}}` {
			t.Fatalf("wrong private native trust: %#v", args)
		}
	}
	t.Setenv(limitedgroups.ProcessEnv, "")
	app := New(Config{Source: "vscode", WorkspaceRoot: "/owner"})
	args, _ := app.buildCodexChildLaunch(base)
	if !reflect.DeepEqual(args, base) {
		t.Fatal("personal launch changed")
	}
}
