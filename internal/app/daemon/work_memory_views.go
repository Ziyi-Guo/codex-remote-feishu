package daemon

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

// Clone only outgoing DTOs; the routing state and source views remain intact.
func (a *App) filterWorkMemoryViewsLocked(event eventcontract.Event) eventcontract.Event {
	if a.headlessRuntime.Paths.StateDir == "" {
		return event
	}
	ids := workMemoryViewThreadIDs(event)
	if len(ids) == 0 {
		return event
	}
	visible := a.workMemoryVisibleThreadsLocked(event.SurfaceSessionID, ids)
	allowed := func(id string) bool { return id == "" || visible[id] }
	if event.Snapshot != nil {
		v := *event.Snapshot
		v.Threads = nil
		for _, thread := range event.Snapshot.Threads {
			if allowed(thread.ThreadID) {
				v.Threads = append(v.Threads, thread)
			}
		}
		if !allowed(v.Attachment.SelectedThreadID) {
			v.Attachment.SelectedThreadID = ""
			v.Attachment.SelectedThreadTitle = ""
			v.Attachment.SelectedThreadFirstUserMessage = ""
			v.Attachment.SelectedThreadLastUserMessage = ""
			v.Attachment.SelectedThreadModelReroute = nil
			v.Attachment.SelectedThreadAgeText = ""
		}
		if !allowed(v.NextPrompt.ThreadID) {
			v.NextPrompt = control.PromptRouteSummary{}
		}
		if !allowed(v.PendingHeadless.ThreadID) {
			v.PendingHeadless = control.PendingHeadlessSummary{}
		}
		v.Instances = append([]control.InstanceSummary(nil), v.Instances...)
		for i := range v.Instances {
			if !allowed(v.Instances[i].ObservedFocusedThreadID) {
				v.Instances[i].ObservedFocusedThreadID = ""
			}
		}
		event.Snapshot = &v
	}
	if event.TargetPickerView != nil {
		v := *event.TargetPickerView
		v.SessionOptions = nil
		for _, option := range event.TargetPickerView.SessionOptions {
			if option.Kind != control.FeishuTargetPickerSessionThread || allowed(strings.TrimPrefix(option.Value, "thread:")) {
				v.SessionOptions = append(v.SessionOptions, option)
			}
		}
		if id, ok := strings.CutPrefix(v.SelectedSessionValue, "thread:"); ok && !allowed(id) {
			v.SelectedSessionValue = ""
			v.SelectedSessionLabel = ""
			v.SelectedSessionMeta = ""
			v.CanConfirm = false
			v.BodySections = nil
			v.NoticeSections = nil
			v.StatusSections = nil
			v.StatusText = ""
			v.StatusTitle = ""
			v.StatusFooter = ""
			v.Messages = nil
			v.SourceMessages = nil
			v.Question = ""
			v.Hint = "请选择其他会话，或新建会话。"
		}
		event.TargetPickerView = &v
	}
	if event.SelectionView != nil {
		v := *event.SelectionView
		if v.Thread != nil {
			threads := *v.Thread
			threads.Entries = nil
			for _, entry := range v.Thread.Entries {
				if allowed(entry.ThreadID) {
					threads.Entries = append(threads.Entries, entry)
				}
			}
			v.Thread = &threads
		}
		if v.KickThread != nil && !allowed(v.KickThread.ThreadID) {
			v.KickThread = &control.FeishuKickThreadSelectionView{ThreadLabel: "个人会话", Hint: "请返回选择其他会话，或使用 /new。", CancelLabel: "返回"}
		}
		event.SelectionView = &v
	}
	if event.SelectionContext != nil && !allowed(event.SelectionContext.Surface.SelectedThreadID) {
		v := *event.SelectionContext
		v.Surface.SelectedThreadID = ""
		v.Title = "选择会话"
		v.ContextTitle = ""
		v.ContextText = ""
		v.ContextKey = ""
		event.SelectionContext = &v
	}
	if event.TargetPickerContext != nil {
		v := *event.TargetPickerContext
		if !allowed(v.Surface.SelectedThreadID) {
			v.Surface.SelectedThreadID = ""
		}
		if id, ok := strings.CutPrefix(v.SelectedSessionValue, "thread:"); ok && !allowed(id) {
			v.SelectedSessionValue = ""
		}
		event.TargetPickerContext = &v
	}
	event.Payload = nil
	event.Payload = event.CanonicalPayload()
	return event
}

func workMemoryViewThreadIDs(event eventcontract.Event) []string {
	ids := map[string]bool{}
	add := func(id string) {
		if id != "" {
			ids[id] = true
		}
	}
	if v := event.Snapshot; v != nil {
		for _, thread := range v.Threads {
			add(thread.ThreadID)
		}
		add(v.Attachment.SelectedThreadID)
		add(v.NextPrompt.ThreadID)
		add(v.PendingHeadless.ThreadID)
		for _, instance := range v.Instances {
			add(instance.ObservedFocusedThreadID)
		}
	}
	if v := event.TargetPickerView; v != nil {
		for _, option := range v.SessionOptions {
			if option.Kind == control.FeishuTargetPickerSessionThread {
				add(strings.TrimPrefix(option.Value, "thread:"))
			}
		}
		if id, ok := strings.CutPrefix(v.SelectedSessionValue, "thread:"); ok {
			add(id)
		}
	}
	if v := event.SelectionView; v != nil {
		if v.Thread != nil {
			for _, entry := range v.Thread.Entries {
				add(entry.ThreadID)
			}
		}
		if v.KickThread != nil {
			add(v.KickThread.ThreadID)
		}
	}
	if v := event.SelectionContext; v != nil {
		add(v.Surface.SelectedThreadID)
	}
	if v := event.TargetPickerContext; v != nil {
		add(v.Surface.SelectedThreadID)
		if id, ok := strings.CutPrefix(v.SelectedSessionValue, "thread:"); ok {
			add(id)
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	return result
}
