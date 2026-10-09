package codexprofile

import (
	"strings"
	"testing"
)

func TestProfileProbeLaunchesUsePrivateStdio(t *testing.T) {
	base := []string{"CODEX_HOME=/existing/codex-home", "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=0"}
	for name, material := range map[string]ProbeLaunchMaterial{
		"oauth":      OAuthProbeLaunchMaterial(base),
		"capability": CapabilityPreflightLaunchMaterial(base, "/disposable/codex-home"),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(strings.Join(material.Args, "\x00"), "--listen=stdio://") {
				t.Fatalf("probe transport is not explicit stdio: %#v", material.Args)
			}
			if lookupProbeTestEnv(material.Env, "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED") != "1" {
				t.Fatal("probe inherited Remote Control enrollment")
			}
		})
	}
	if base[1] != "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=0" {
		t.Fatal("probe modified parent environment")
	}
}
