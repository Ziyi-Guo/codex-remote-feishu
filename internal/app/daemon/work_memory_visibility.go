package daemon

import (
	"os"
	"path/filepath"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// One bounded ownership read per outgoing view, not one disk read per row.
func (a *App) workMemoryVisibleThreadsLocked(surface string, ids []string) map[string]bool {
	if a.workMemory == nil {
		a.workMemory = &workMemoryRuntime{root: filepath.Join(a.headlessRuntime.Paths.StateDir, "work-memory"), lifecycle: a.daemonLifecycleID, busy: make(chan struct{}, 1)}
	}
	req := a.workMemoryViewerRequestLocked(surface, a.service.AttachedInstanceID(surface), "")
	runtime := a.workMemory
	a.mu.Unlock()
	visible := runtime.visibleThreads(req, ids)
	a.mu.Lock()
	current := a.workMemoryViewerRequestLocked(surface, a.service.AttachedInstanceID(surface), "")
	if current.Actor != req.Actor || current.Authorized != req.Authorized {
		return map[string]bool{}
	}
	return visible
}
func (r *workMemoryRuntime) visibleThreads(req workMemoryRequest, ids []string) map[string]bool {
	fallback := map[string]bool{}
	for _, id := range ids {
		candidate := req
		candidate.Command.Target = agentproto.Target{ThreadID: id}
		fallback[id] = r.ordinaryAllows(candidate)
	}
	select {
	case r.busy <- struct{}{}:
	default:
		return fallback
	}
	result := make(chan map[string]bool, 1)
	go func() { value := r.readVisibleThreads(req, ids, fallback); <-r.busy; result <- value }()
	select {
	case visible := <-result:
		return visible
	case <-time.After(r.ioBudget()):
		return fallback
	}
}

func (r *workMemoryRuntime) readVisibleThreads(req workMemoryRequest, ids []string, fallback map[string]bool) map[string]bool {
	visible := map[string]bool{}
	root, err := openWorkMemoryRoot(r.root)
	if os.IsNotExist(err) && workMemoryRootMayBeAbsent(r.root) {
		for _, id := range ids {
			visible[id] = true
		}
		return visible
	}
	if err != nil {
		return fallback
	}
	defer root.Close()
	owners, err := readWorkMemoryOwners(root)
	if err != nil {
		return fallback
	}
	for _, id := range ids {
		candidate := req
		candidate.Command.Target = agentproto.Target{ThreadID: id}
		candidate.Ordinary = r.ordinaryAllows(candidate)
		visible[id] = owners.guard(candidate) == nil
	}
	return visible
}
