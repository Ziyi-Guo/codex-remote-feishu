package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/buildinfo"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestFormatStatusSnapshotBinaryIncludesFlavor(t *testing.T) {
	previous := buildinfo.FlavorValue
	buildinfo.FlavorValue = string(buildinfo.FlavorShipping)
	t.Cleanup(func() {
		buildinfo.FlavorValue = previous
	})

	got := formatStatusSnapshotBinary(agentproto.ServerIdentity{
		BinaryIdentity: agentproto.BinaryIdentity{
			Branch:           "master",
			Version:          "dev-9ae95f7175a5",
			BuildFingerprint: "sha256:200123456789",
		},
	})
	if !strings.Contains(got, "master / dev-9ae95f7175a5 / sha256:200") {
		t.Fatalf("formatted binary = %q, want branch/version/fingerprint", got)
	}
	if !strings.Contains(got, "flavor:shipping") {
		t.Fatalf("formatted binary = %q, want flavor suffix", got)
	}
}

func TestNextCommandIDDoesNotRepeatAcrossDaemonLifecycles(t *testing.T) {
	first := New("", "", nil, agentproto.ServerIdentity{PID: 42, StartedAt: time.Unix(1, 0)})
	restarted := New("", "", nil, agentproto.ServerIdentity{PID: 42, StartedAt: time.Unix(2, 0)})
	oldID, newID := first.nextCommandID(), restarted.nextCommandID()
	if oldID == newID {
		t.Fatal("late old-wrapper response can match new daemon command")
	}
	if !strings.Contains(oldID, first.daemonLifecycleID) || !strings.Contains(newID, restarted.daemonLifecycleID) {
		t.Fatal("production command IDs lack lifecycle identity")
	}
	if next := restarted.nextCommandID(); next == newID || !strings.HasSuffix(next, "-2") {
		t.Fatal("lifecycle command sequence not monotonic")
	}
}
func TestNextCommandIDBareAppKeepsLegacyFixtureCompatibility(t *testing.T) {
	app := &App{}
	if first, second := app.nextCommandID(), app.nextCommandID(); first != "cmd-1" || second != "cmd-2" {
		t.Fatalf("bare App fixture IDs changed: %q %q", first, second)
	}
}
