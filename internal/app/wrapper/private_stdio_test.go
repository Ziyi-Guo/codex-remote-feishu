package wrapper

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func TestRealCodexChildLaunchUsesPrivateStdio(t *testing.T) {
	binary := os.Getenv("CODEX_TEST_REAL_BINARY")
	if binary == "" {
		t.Skip("CODEX_TEST_REAL_BINARY is not set")
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED", "0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--enable", "fast_mode", "app-server", "--disable", "shell_snapshot", "-c", `model_reasoning_effort="max"`}
	app := New(Config{Backend: agentproto.BackendCodex, CodexRealBinary: binary, Args: args, WorkspaceRoot: t.TempDir(), Source: "headless", Managed: true, RuntimePaths: relayruntime.Paths{StateDir: t.TempDir()}})
	session, err := app.launchCodexChildSession(ctx, nil, func(agentproto.ErrorInfo) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		session.cancel()
		select {
		case <-session.waitErr:
		case <-time.After(3 * time.Second):
			t.Error("private child did not exit after cancellation")
		}
	}()
	if !strings.Contains(strings.Join(session.cmd.Args, "\x00"), "--listen=stdio://") {
		t.Fatalf("real child was not launched with private stdio: %#v", session.cmd.Args)
	}
	joined := strings.Join(session.cmd.Env, "\n")
	if !strings.Contains(joined, "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=1") || strings.Contains(joined, "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=0") {
		t.Fatal("real child inherited Remote Control enrollment")
	}
	if !strings.Contains(joined, "CODEX_HOME="+codexHome) {
		t.Fatal("private transport changed Codex home")
	}
	if os.Getenv("CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED") != "0" || strings.Join(app.config.Args, "\x00") != strings.Join(args, "\x00") {
		t.Fatal("child launch changed parent preferences or arguments")
	}
}
