package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func TestLimitedGroupsWorkdirStopsAncestorInstructions(t *testing.T) {
	a, _, action := limitedDaemonFixture(t)
	admitted, ok := a.guardLimitedInbound(action)
	if !ok {
		t.Fatal("valid admission rejected")
	}
	marker := filepath.Join(admitted.LimitedExecution.Workdir, limitedgroups.SessionRootMarker)
	st, err := os.Lstat(marker)
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("isolated instruction root missing: %v", err)
	}
	if got := admitted.LimitedExecution.ConfigOverrides()["project_root_markers"]; !reflect.DeepEqual(got, []string{limitedgroups.SessionRootMarker}) {
		t.Fatalf("ancestor state instructions still visible: %#v", got)
	}
}

func TestLimitedGroupsHeadlessUsesCanonicalNativeExecutableOnly(t *testing.T) {
	_, _, action := limitedDaemonFixture(t)
	root := t.TempDir()
	real := filepath.Join(root, "native-codex")
	link := filepath.Join(root, "codex")
	if err := os.WriteFile(real, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	env := []string{"KEEP=1", config.CodexRealBinaryEnv + "=" + link}
	got, err := limitedHeadlessEnv(action.SurfaceSessionID, env, "unused")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"KEEP=1", config.CodexRealBinaryEnv + "=" + canonical}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native helper retains blocked symlink: %#v", got)
	}
	ordinary, err := limitedHeadlessEnv("feishu:app:chat:oc_other@om_topic", env, "unused")
	if err != nil || !reflect.DeepEqual(ordinary, env) {
		t.Fatal("personal launch changed")
	}
	if _, err := limitedHeadlessEnv(action.SurfaceSessionID, nil, filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing native runtime accepted")
	}
}
