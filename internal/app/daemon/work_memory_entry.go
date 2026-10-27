package daemon

import (
	"os"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// This fixed local configuration is operator-authored; project archives never
// become developer instructions. Retrieval remains an agent tool decision.
func (r *workMemoryRuntime) prepareEntry(root *os.Root, path string, owners workMemoryOwners, req workMemoryRequest, inheritedPrivate bool) workMemoryResult {
	if path != "runtime-guide.json" || req.Backend != agentproto.BackendCodex {
		return workMemoryResult{}
	}
	var guide struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Instruction   string `json:"instruction"`
	}
	if workMemoryReadJSON(root, path, 4<<10, &guide) != nil || guide.SchemaVersion != 1 || guide.Kind != "runtime_entry" || strings.TrimSpace(guide.Instruction) == "" {
		return workMemoryResult{}
	}
	if !inheritedPrivate {
		if owners.claim(req, r.lifecycle) != nil || writeWorkMemoryOwners(r.root, owners) != nil {
			return workMemoryResult{}
		}
	}
	return workMemoryResult{DeveloperInstruction: strings.TrimSpace(guide.Instruction)}
}
