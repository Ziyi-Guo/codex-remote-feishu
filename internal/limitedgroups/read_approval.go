package limitedgroups

// IsKnownReadApprovalCommand examines the complete native command string, never
// commandActions (a display summary) or an exec-policy prefix amendment.
func IsKnownReadApprovalCommand(command string) bool {
	if IsKnownReadCommand(command) {
		return true
	}
	argv, ok := literalArgv(command)
	return ok && len(argv) == 3 && oneOf(argv[0], "/bin/sh", "/bin/bash", "/bin/zsh") && oneOf(argv[1], "-c", "-lc") && IsKnownReadCommand(argv[2])
}
