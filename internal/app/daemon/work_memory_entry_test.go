package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func memoryEntryFixture(t *testing.T) (*App, agentproto.Command, string) {
	t.Helper()
	a, c, root := memoryFixture(t)
	memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "mode": "on_demand", "guide_path": "runtime-guide.json", "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface}}})
	memoryWrite(t, root, "runtime-guide.json", map[string]any{"schema_version": 1, "kind": "runtime_entry", "instruction": "需要项目背景时，按问题调用本机项目档案 CLI；是否读取由你判断。"})
	c.CodexResume = &agentproto.CodexResumePolicy{ModelProviderID: "openai", DeveloperInstruction: "existing profile rule"}
	return a, c, root
}

func TestWorkMemoryOnDemandKeepsPrivateOwnerAfterDisablingEntry(t *testing.T) {
	a, c, root := memoryEntryFixture(t)
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("owner could not dispatch")
	}
	memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": false})
	other := "feishu:app-1:user:other"
	a.service.MaterializeSurface(other, "app-1", "other-chat", "other")
	s := a.service.Surface(other)
	s.AttachedInstanceID, s.ActiveQueueItemID = "instance", "queue"
	s.QueueItems = map[string]*state.QueueItemRecord{"queue": {ID: "queue", ActorUserID: "other", SourceKind: state.QueueItemSourceUser, SourceMessageID: "other-message"}}
	c.Origin = agentproto.Origin{Surface: other, ChatID: "other-chat", UserID: "other", MessageID: "other-message"}
	if got := memoryDispatch(t, a, c); len(got) != 0 {
		t.Fatalf("private thread escaped after disable: %+v", got)
	}
}

func TestWorkMemoryOnDemandSameOwnerCannotMovePrivateThreadToAnotherBoundSurface(t *testing.T) {
	a, c, root := memoryEntryFixture(t)
	other := "feishu:app-1:chat:other-chat"
	memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "mode": "on_demand", "guide_path": "runtime-guide.json", "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface}, map[string]any{"surface_id": other}}})
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("original surface could not dispatch")
	}
	a.service.MaterializeSurface(other, "app-1", "other-chat", "owner")
	s := a.service.Surface(other)
	s.AttachedInstanceID, s.ActiveQueueItemID = "instance", "queue"
	s.QueueItems = map[string]*state.QueueItemRecord{"queue": {ID: "queue", ActorUserID: "owner", SourceKind: state.QueueItemSourceUser, SourceMessageID: "other-message"}}
	c.Origin = agentproto.Origin{Surface: other, ChatID: "other-chat", UserID: "owner", MessageID: "other-message"}
	if !a.workMemoryRequestLocked(other, "instance", c).Authorized {
		t.Fatal("fixture must prove otherwise-authorized owner on another bound surface")
	}
	if got := memoryDispatch(t, a, c); len(got) != 0 {
		t.Fatalf("same owner moved private thread: %+v", got)
	}
}

func TestWorkMemoryOnDemandUsesThreadInstructionsWithoutProjectInputs(t *testing.T) {
	a, c, root := memoryEntryFixture(t)
	sent := memoryDispatch(t, a, c)
	if len(sent) != 1 || !reflect.DeepEqual(sent[0].Prompt.Inputs, c.Prompt.Inputs) {
		t.Fatalf("on-demand changed user inputs: %+v", sent)
	}
	if !strings.Contains(sent[0].CodexResume.DeveloperInstruction, "existing profile rule") || !strings.Contains(sent[0].CodexResume.DeveloperInstruction, "项目档案 CLI") || strings.Contains(sent[0].CodexResume.DeveloperInstruction, "记忆一") {
		t.Fatalf("unexpected runtime entry: %+v", sent[0].CodexResume)
	}
	if c.CodexResume.DeveloperInstruction != "existing profile rule" {
		t.Fatal("mutated queued profile")
	}
	ownersRoot, err := openWorkMemoryRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ownersRoot.Close()
	owners, err := readWorkMemoryOwners(ownersRoot)
	if err != nil || owners.Threads["thread"].Surface != memorySurface {
		t.Fatalf("entry thread was not protected: %+v %v", owners, err)
	}
}

func TestWorkMemoryOnDemandFailClosedEntryGates(t *testing.T) {
	for _, name := range []string{"wrong_actor", "wrong_surface", "unknown_mode", "invalid_guide", "external_guide"} {
		t.Run(name, func(t *testing.T) {
			a, c, root := memoryEntryFixture(t)
			switch name {
			case "wrong_actor":
				c.Origin.UserID = "other"
			case "wrong_surface":
				memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "mode": "on_demand", "guide_path": "runtime-guide.json", "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": "other"}}})
			case "unknown_mode", "external_guide":
				mode, guide := "on_demand", "runtime-guide.json"
				if name == "unknown_mode" {
					mode = "future"
				} else {
					guide = "works/work/context.json"
				}
				memoryWrite(t, root, "manifest.json", map[string]any{"schema_version": 1, "enabled": true, "mode": mode, "guide_path": guide, "owner_user_id": "owner", "bindings": []any{map[string]any{"surface_id": memorySurface}}})
			case "invalid_guide":
				memoryWrite(t, root, "runtime-guide.json", map[string]any{"schema_version": 1, "kind": "project_context", "instruction": "untrusted"})
			}
			sent := memoryDispatch(t, a, c)
			if len(sent) != 1 || !reflect.DeepEqual(sent[0].Prompt.Inputs, c.Prompt.Inputs) || (sent[0].CodexResume != nil && sent[0].CodexResume.DeveloperInstruction != "existing profile rule") {
				t.Fatalf("ineligible entry escaped: %+v", sent)
			}
		})
	}
}

func TestWorkMemoryOnDemandNativeDefaultUsesIndependentEntry(t *testing.T) {
	a, c, _ := memoryEntryFixture(t)
	c.CodexResume = nil
	sent := memoryDispatch(t, a, c)
	if len(sent) != 1 || sent[0].CodexResume != nil || !reflect.DeepEqual(sent[0].Prompt.Inputs, c.Prompt.Inputs) {
		t.Fatalf("native default was changed: %+v", sent)
	}
	b, _ := json.Marshal(sent[0])
	var wire map[string]any
	_ = json.Unmarshal(b, &wire)
	instruction, _ := wire["developerInstructionAppend"].(string)
	if !strings.Contains(instruction, "项目档案 CLI") {
		t.Fatal("native default silently lost entry")
	}
}
