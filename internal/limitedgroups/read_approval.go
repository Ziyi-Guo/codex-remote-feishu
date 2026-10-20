package limitedgroups

// IsKnownReadApprovalCommand examines the complete native command string, never
// commandActions (a display summary) or an exec-policy prefix amendment.
func IsKnownReadApprovalCommand(command string) bool {
	count := 0
	return knownReadProgram(command, true, &count)
}
