package limitedgroups

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const forbiddenRulesFilename = "profc-forbidden.rules"
const pilotIPGenRelativePath = ".local/state/codex-remote/deliverables/ipgen-cli-authoring-20261019/registry-macos/prefix/bin/ipgen"

// ForbiddenRules returns native execpolicy rules, never shell commands.
// shortcut: these are bounded literal prefixes, not a shell or SQL firewall;
// review the list when CLI command paths or global options change.
func ForbiddenRules() string {
	var out strings.Builder
	out.WriteString("# Private prof.C pilot rules. Explicit write prefixes only.\n")
	out.WriteString("# RDS query remains available; its CLI validates read-only SQL before API calls.\n")
	emit := func(pattern []any) {
		encoded, _ := json.Marshal(pattern)
		fmt.Fprintf(&out, "prefix_rule(pattern=%s, decision=\"forbidden\", justification=\"prof.C pilot: explicit write/generation command is forbidden\")\n", encoded)
	}
	writeVerbs := []string{"create", "update", "delete", "insert", "execute", "exec", "execute-sql", "exec-sql", "write", "alter", "drop", "truncate", "grant", "revoke", "apply"}
	rds := [][]any{
		{"rds", writeVerbs},
		{"rds", []string{"bpm-create", "bpm-cancel", "bpm-update-sql", "bpm-apply-permission"}},
		{"rds", "db", writeVerbs},
		{"rds", "db", "table", writeVerbs},
		{"rds", "bpm", []string{"create", "cancel", "update", "execute", "apply-permission", "update-sql", "create-ddl", "create-dml"}},
		{"rds", "bpm", "permission", []string{"apply", "create", "update", "delete"}},
		{"rds", "bpm", "change-record", []string{"create", "execute", "cancel", "update"}},
		{"rds", "bpm", "dbw", "ticket", []string{"approve", "cancel", "create-ddl", "create-dml", "execute", "submit"}},
		{"rds", "bpm", "ttp-review", "assign"},
	}
	globalPrefixes := forbiddenGlobalPrefixes()
	bytedcliNames := forbiddenExecutableNames("bytedcli")
	for _, path := range rds {
		for _, globals := range globalPrefixes {
			emit(append(append([]any{bytedcliNames}, globals...), path...))
		}
		// Also cover the same common options placed at one subcommand boundary.
		for boundary := 1; boundary < len(path); boundary++ {
			for _, globals := range globalPrefixes[1:] {
				pattern := append([]any{bytedcliNames}, path[:boundary]...)
				pattern = append(pattern, globals...)
				emit(append(pattern, path[boundary:]...))
			}
		}
	}
	ipgen := [][]any{
		{[]string{"create", "update", "delete", "generate", "repaint", "compose", "plan", "import", "replace", "upload"}},
		{[]string{"show", "project", "film", "chapter", "scene", "character", "appearance", "asset", "episode", "shot", "storyboard", "script", "content", "task"}, []string{"create", "update", "delete", "generate", "repaint", "compose", "plan", "confirm", "import", "replace", "upload", "create-upload", "update-prompt", "submit", "retry", "cancel"}},
		{"shot", []string{"image", "video"}},
		{[]string{"show", "project", "episode", "chapter", "scene"}, []string{"asset", "shot", "character", "scene", "storyboard"}, []string{"create", "update", "delete", "generate", "repaint", "compose", "plan", "import", "replace", "upload"}},
	}
	ipgenNames := forbiddenExecutableNames("ipgen", "ipgen-cli")
	for _, path := range ipgen {
		for _, globals := range globalPrefixes {
			pattern := append([]any{ipgenNames}, globals...)
			emit(append(pattern, path...))
		}
	}
	methods := []string{"POST", "PUT", "DELETE", "PATCH"}
	var joined []string
	for _, method := range methods {
		joined = append(joined, "-X"+method, "--request="+method)
	}
	curlFlags := []string{"-s", "-S", "-sS", "-f", "-fsS", "-L", "-sSL", "--silent", "--show-error", "--fail", "--location"}
	curlPrefixes := [][]any{{}}
	for n := 1; n <= 2; n++ {
		prefix := make([]any, n)
		for i := range prefix {
			prefix[i] = curlFlags
		}
		curlPrefixes = append(curlPrefixes, prefix)
	}
	for _, flags := range curlPrefixes {
		prefix := append([]any{forbiddenExecutableNames("curl")}, flags...)
		emit(append(append([]any{}, prefix...), []string{"-X", "--request"}, methods))
		emit(append(append([]any{}, prefix...), joined))
	}
	return out.String()
}

func forbiddenExecutableNames(names ...string) []string {
	var paths []string
	add := func(path string) {
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	for _, name := range names {
		add(name)
		// launchd can have a smaller PATH than an interactive native shell.
		// Include installed Homebrew spellings before resolving PATH entries.
		for _, binDir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
			path := filepath.Join(binDir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				add(path)
				if canonical, err := filepath.EvalSymlinks(path); err == nil {
					add(canonical)
				}
			}
		}
		if path, err := exec.LookPath(name); err == nil && filepath.IsAbs(path) {
			add(path)
			if canonical, err := filepath.EvalSymlinks(path); err == nil {
				add(canonical)
			}
		}
		if name == "ipgen" {
			if home, err := os.UserHomeDir(); err == nil {
				path := filepath.Join(home, pilotIPGenRelativePath)
				add(path)
				if canonical, err := filepath.EvalSymlinks(path); err == nil {
					add(canonical)
				}
			}
		}
	}
	return paths
}

// Each common option appears at most once, in any order, at one boundary.
func forbiddenGlobalPrefixes() [][]any {
	sites := []string{"cn", "prod", "boe", "i18n", "i18n-bd", "i18n-tt", "us-ttp", "us-ttp-bdee", "us-ttp-usts", "eu-ttp", "eu-ttp-limited", "eu-ttp-full"}
	var assignedSites []string
	for _, site := range sites {
		assignedSites = append(assignedSites, "--site="+site)
	}
	options := [][][]any{
		{{[]string{"--json", "-j"}}},
		{{"--no-auto-upgrade"}},
		{{"--site", sites}, {assignedSites}},
	}
	var prefixes [][]any
	var visit func([]any, uint)
	visit = func(prefix []any, used uint) {
		prefixes = append(prefixes, prefix)
		for i, variants := range options {
			if used&(1<<i) != 0 {
				continue
			}
			for _, variant := range variants {
				next := append(append([]any{}, prefix...), variant...)
				visit(next, used|(1<<i))
			}
		}
	}
	visit(nil, 0)
	return prefixes
}

// EnsureForbiddenRules only writes beneath an existing canonical private cwd.
// A differing existing file fails closed rather than replacing owner content.
func EnsureForbiddenRules(workdir string) error {
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir || workdir == string(filepath.Separator) {
		return fmt.Errorf("invalid private rules workdir")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return err
	}
	defer func() { root.Close() }()
	for _, component := range strings.Split(strings.TrimPrefix(workdir, string(filepath.Separator)), string(filepath.Separator)) {
		next, err := forbiddenOpenDirectory(root, component, false)
		if err != nil {
			return err
		}
		root.Close()
		root = next
	}
	for _, component := range []string{".", ".codex", "rules"} {
		next, err := forbiddenOpenDirectory(root, component, true)
		if err != nil {
			return err
		}
		root.Close()
		root = next
	}
	data := ForbiddenRules()
	info, err := root.Lstat(forbiddenRulesFilename)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("private rules must be a regular file with mode 0600")
		}
		f, err := root.Open(forbiddenRulesFilename)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return fmt.Errorf("private rules changed while opening")
		}
		contents, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
		if err != nil {
			return err
		}
		if string(contents) != data {
			return fmt.Errorf("existing private rules differ from expected policy")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	f, err := root.OpenFile(forbiddenRulesFilename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(f, data)
	closeErr := f.Close()
	if writeErr != nil {
		root.Remove(forbiddenRulesFilename)
		return writeErr
	}
	return closeErr
}

func forbiddenOpenDirectory(root *os.Root, name string, private bool) (*os.Root, error) {
	if private && name != "." {
		if err := root.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private rules directory must not be a symlink: %s", name)
	}
	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := next.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		next.Close()
		return nil, fmt.Errorf("private rules directory changed while opening")
	}
	if private {
		f, err := next.Open(".")
		if err == nil {
			err = f.Chmod(0700)
			f.Close()
		}
		if err != nil {
			next.Close()
			return nil, err
		}
	}
	return next, nil
}
