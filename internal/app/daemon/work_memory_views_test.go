package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestWorkMemoryFiltersPrivateThreadMetadataInOutboundCopies(t *testing.T) {
	a, c, _ := memoryFixture(t)
	memoryDispatch(t, a, c)
	other := "feishu:app-1:user:other"
	a.service.MaterializeSurface(other, "app-1", "other-chat", "other")
	const secret = "SYNTHETIC_PRIVATE_MEMORY_SECRET"
	target := control.FeishuTargetPickerView{SelectedSessionValue: "thread:thread", SelectedSessionLabel: secret, SelectedSessionMeta: secret, CanConfirm: true, AllowNewThread: true, BodySections: []control.FeishuCardTextSection{{Label: "会话", Lines: []string{secret}}}, SessionOptions: []control.FeishuTargetPickerSessionOption{{Value: "thread:thread", Kind: control.FeishuTargetPickerSessionThread, Label: secret, MetaText: secret}, {Value: "thread:normal", Kind: control.FeishuTargetPickerSessionThread, Label: "普通会话"}, {Value: "new_thread", Kind: control.FeishuTargetPickerSessionNewThread, Label: "新建会话"}}}
	selection := control.FeishuSelectionView{Thread: &control.FeishuThreadSelectionView{Entries: []control.FeishuThreadSelectionEntry{{ThreadID: "thread", Summary: secret}, {ThreadID: "normal", Summary: "普通会话"}}}}
	snapshot := control.Snapshot{Attachment: control.AttachmentSummary{SelectedThreadID: "thread", SelectedThreadTitle: secret, SelectedThreadFirstUserMessage: secret, SelectedThreadLastUserMessage: secret}, NextPrompt: control.PromptRouteSummary{ThreadID: "thread", ThreadTitle: secret}, Threads: []control.ThreadSummary{{ThreadID: "thread", Name: secret, Preview: secret}, {ThreadID: "normal", Name: "普通会话"}}}
	events := []eventcontract.Event{{SurfaceSessionID: other, Payload: eventcontract.TargetPickerPayload{View: target}}, {SurfaceSessionID: other, Payload: eventcontract.SelectionPayload{View: selection}}, {SurfaceSessionID: other, Payload: eventcontract.SnapshotPayload{Snapshot: snapshot}}}
	for _, event := range events {
		original, _ := json.Marshal(event)
		a.mu.Lock()
		got := a.filterWorkMemoryViewsLocked(event.Normalized())
		a.mu.Unlock()
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), secret) {
			t.Fatalf("private metadata escaped: %s", data)
		}
		if !strings.Contains(string(data), "普通会话") {
			t.Fatal("normal thread removed")
		}
		if event.Payload.Kind() == eventcontract.KindTargetPicker && !strings.Contains(string(data), "new_thread") {
			t.Fatal("new thread escape removed")
		}
		after, _ := json.Marshal(event)
		if string(original) != string(after) {
			t.Fatal("shared core view mutated")
		}
		event.SurfaceSessionID = memorySurface
		a.mu.Lock()
		own := a.filterWorkMemoryViewsLocked(event.Normalized())
		a.mu.Unlock()
		ownData, _ := json.Marshal(own)
		if !strings.Contains(string(ownData), secret) {
			t.Fatal("owner private metadata hidden")
		}
	}
}
