package daemon

import "github.com/kxn/codex-remote-feishu/internal/core/eventcontract"

func (a *App) workMemoryUIEventAllowedLocked(event eventcontract.Event) bool {
	if a.headlessRuntime.Paths.StateDir == "" {
		return true
	}
	threads := []string{}
	if event.Block != nil {
		threads = append(threads, event.Block.ThreadID)
	}
	if event.TimelineText != nil {
		threads = append(threads, event.TimelineText.ThreadID)
	}
	if event.ImageOutput != nil {
		threads = append(threads, event.ImageOutput.ThreadID)
	}
	if event.ThreadHistoryView != nil {
		threads = append(threads, event.ThreadHistoryView.ThreadID)
	}
	if event.ThreadHistoryContext != nil {
		threads = append(threads, event.ThreadHistoryContext.ThreadID)
	}
	if event.ThreadSelection != nil {
		threads = append(threads, event.ThreadSelection.ThreadID)
	}
	if event.ExecCommandProgress != nil {
		threads = append(threads, event.ExecCommandProgress.ThreadID)
	}
	if event.PlanUpdate != nil {
		threads = append(threads, event.PlanUpdate.ThreadID)
	}
	if event.FileChangeSummary != nil {
		threads = append(threads, event.FileChangeSummary.ThreadID)
	}
	if event.TurnDiffSnapshot != nil {
		threads = append(threads, event.TurnDiffSnapshot.ThreadID)
	}
	if event.RequestContext != nil {
		threads = append(threads, event.RequestContext.ThreadID)
	}
	if event.RequestView != nil {
		threads = append(threads, event.RequestView.ThreadID)
	}
	seen := map[string]bool{}
	for _, thread := range threads {
		if thread != "" && !seen[thread] {
			seen[thread] = true
			if a.guardWorkMemoryHistoryLocked(event.SurfaceSessionID, a.service.AttachedInstanceID(event.SurfaceSessionID), thread) != nil {
				return false
			}
		}
	}
	return true
}
