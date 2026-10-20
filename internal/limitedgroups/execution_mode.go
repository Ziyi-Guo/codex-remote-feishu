package limitedgroups

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const ReadOnlyNetworkNever = "readonly-network-never"
const ExecutionModeEnv = "CODEX_REMOTE_LIMITED_EXECUTION_MODE"

func validExecutionMode(mode string) bool { return mode == "" || mode == ReadOnlyNetworkNever }

func (p ExecutionPolicy) ReadOnlyNetworkNever() bool { return p.ExecutionMode == ReadOnlyNetworkNever }

// ForbiddenRulesSHA256 freezes the daemon-installed file for this private process.
// Validation does not rediscover executable paths from a different wrapper PATH.
func ForbiddenRulesSHA256(workdir string) (string, error) {
	path := filepath.Join(workdir, ".codex", "rules", forbiddenRulesFilename)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", fmt.Errorf("private rules must not contain symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", fmt.Errorf("private rules require a regular 0600 file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
