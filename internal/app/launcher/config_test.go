package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConfigCommandDispatch(t *testing.T) {
	decision, err := Detect([]string{"config", "check", "--config", "/tmp/config.json"})
	if err != nil || decision.Role != RoleConfig || strings.Join(decision.Args, " ") != "check --config /tmp/config.json" {
		t.Fatalf("decision %#v, %v", decision, err)
	}
	for _, fails := range []bool{false, true} {
		var out, stderr bytes.Buffer
		called := false
		code := Main(Options{Args: []string{"config", "apply", "--config", "/tmp/config.json"}, Stdout: &out, Stderr: &stderr, Runners: RunnerSet{RunConfig: func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
			called = true
			if strings.Join(args, " ") != "apply --config /tmp/config.json" {
				t.Errorf("args %#v", args)
			}
			if fails {
				return errors.New("apply rejected")
			}
			return nil
		}}})
		if !called {
			t.Fatal("config runner not called")
		}
		if fails && (code != 1 || !strings.Contains(stderr.String(), "apply rejected")) {
			t.Fatalf("failed runner: %d %q", code, stderr.String())
		}
		if !fails && code != 0 {
			t.Fatalf("success runner: %d", code)
		}
	}
	if !strings.Contains(usageText(), "config <check|apply>") {
		t.Fatal("missing config help")
	}
}
