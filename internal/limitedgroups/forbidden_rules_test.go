package limitedgroups

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func privateRulesWorkdir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsureForbiddenRulesPrivateAndIdempotent(t *testing.T) {
	work := privateRulesWorkdir(t)
	for i := 0; i < 2; i++ {
		if err := EnsureForbiddenRules(work); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range map[string]os.FileMode{".codex": 0700, ".codex/rules": 0700, ".codex/rules/profc-forbidden.rules": 0600} {
		info, err := os.Lstat(filepath.Join(work, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: info=%v err=%v", name, info, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(work, ".codex/rules/profc-forbidden.rules"))
	if err != nil || string(data) != ForbiddenRules() {
		t.Fatalf("unexpected rules: %v", err)
	}
}

func TestEnsureForbiddenRulesRejectsSymlinksAndChangedContent(t *testing.T) {
	for _, target := range []string{"work", ".codex", ".codex/rules", ".codex/rules/profc-forbidden.rules"} {
		t.Run(target, func(t *testing.T) {
			work, outside := privateRulesWorkdir(t), privateRulesWorkdir(t)
			link := filepath.Join(work, target)
			if target == "work" {
				link = work + "-link"
				work = link
				t.Cleanup(func() { os.Remove(link) })
			}
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if err := EnsureForbiddenRules(work); err == nil {
				t.Fatal("accepted symlink")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("changed symlink destination: %v %v", entries, err)
			}
		})
	}
	work := privateRulesWorkdir(t)
	if err := EnsureForbiddenRules(work); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, ".codex/rules/profc-forbidden.rules")
	if err := os.WriteFile(path, []byte("owner content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureForbiddenRules(work); err == nil {
		t.Fatal("overwrote differing content")
	}
	if data, _ := os.ReadFile(path); string(data) != "owner content" {
		t.Fatal("changed owner content")
	}
	if err := os.WriteFile(path, []byte(ForbiddenRules()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureForbiddenRules(work); err == nil {
		t.Fatal("accepted non-private existing file")
	}
}

func TestEnsureForbiddenRulesRejectsInvalidWorkdir(t *testing.T) {
	for _, work := range []string{"", ".", "/", privateRulesWorkdir(t) + "/missing", privateRulesWorkdir(t) + "/../other"} {
		if err := EnsureForbiddenRules(work); err == nil {
			t.Fatalf("accepted %q", work)
		}
	}
}

func TestForbiddenRulesNativePrefixes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(home, ".codex/packages/standalone/releases/0.160.1-aarch64-apple-darwin/bin/codex")
	if configured := os.Getenv("CODEX_EXECPOLICY_TEST_BINARY"); configured != "" {
		native = configured
	}
	if _, err := os.Stat(native); err != nil {
		t.Skip("native execpolicy CLI unavailable; set CODEX_EXECPOLICY_TEST_BINARY")
	}
	work := privateRulesWorkdir(t)
	if err := EnsureForbiddenRules(work); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(work, ".codex/rules/profc-forbidden.rules")
	cases := []struct {
		argv      []string
		forbidden bool
	}{
		{[]string{"bytedcli", "rds", "bpm", "create"}, true},
		{[]string{"bytedcli", "--json", "--no-auto-upgrade", "--site", "cn", "rds", "bpm", "execute", "123"}, true},
		{[]string{"bytedcli", "--site=boe", "-j", "rds", "bpm", "permission", "apply"}, true},
		{[]string{"bytedcli", "rds", "bpm", "update-sql"}, true},
		{[]string{"bytedcli", "rds", "bpm-create"}, true},
		{[]string{"bytedcli", "rds", "bpm-apply-permission"}, true},
		{[]string{"bytedcli", "rds", "bpm", "--no-auto-upgrade", "--site", "cn", "-j", "cancel"}, true},
		{[]string{"bytedcli", "rds", "bpm", "change-record", "execute"}, true},
		{[]string{"bytedcli", "rds", "bpm", "dbw", "ticket", "create-ddl"}, true},
		{[]string{"bytedcli", "rds", "db", "execute"}, true},
		{[]string{"ipgen", "show", "create"}, true},
		{[]string{"ipgen-cli", "episode", "compose"}, true},
		{[]string{"ipgen", "asset", "update-prompt"}, true},
		{[]string{"ipgen", "storyboard", "replace"}, true},
		{[]string{filepath.Join(home, ".local/state/codex-remote/deliverables/ipgen-cli-authoring-20261019/registry-macos/prefix/bin/ipgen"), "shot", "video"}, true},
		{[]string{"curl", "-X", "POST", "https://example.invalid"}, true},
		{[]string{"curl", "-sS", "--request", "PATCH", "https://example.invalid"}, true},
		{[]string{"curl", "-XPUT", "https://example.invalid"}, true},
		{[]string{"curl", "--request=DELETE", "https://example.invalid"}, true},
		{[]string{"bytedcli", "rds", "db", "query", "demo", "SELECT 1"}, false},
		{[]string{"bytedcli", "--json", "--site", "cn", "rds", "db", "list"}, false},
		{[]string{"bytedcli", "rds", "bpm", "get", "123"}, false},
		{[]string{"ipgen", "show", "create-status"}, false},
		{[]string{"ipgen", "storyboard", "validate"}, false},
		{[]string{"curl", "-X", "GET", "https://example.invalid"}, false},
		{[]string{"curl", "https://example.invalid"}, false},
	}
	for _, name := range []string{"bytedcli", "curl"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, executable := range []string{path, canonical} {
			argv := []string{executable, "-XPOST"}
			if name == "bytedcli" {
				argv = []string{executable, "-j", "rds", "bpm", "cancel"}
			}
			cases = append(cases, struct {
				argv      []string
				forbidden bool
			}{argv, true})
		}
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			args := append([]string{"execpolicy", "check", "--rules", rules, "--"}, tc.argv...)
			out, err := exec.Command(native, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("policy check: %v: %s", err, out)
			}
			var result struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("decode: %v: %s", err, out)
			}
			if got := result.Decision == "forbidden"; got != tc.forbidden {
				t.Fatalf("forbidden=%v want %v: %s", got, tc.forbidden, out)
			}
		})
	}
}
