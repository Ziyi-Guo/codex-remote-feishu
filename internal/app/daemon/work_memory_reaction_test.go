package daemon

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

func TestWorkMemoryReactionKeepsPrivateReplyOwner(t *testing.T) {
	a, c, _ := memoryEntryFixture(t)
	if len(memoryDispatch(t, a, c)) != 1 {
		t.Fatal("owner dispatch failed")
	}
	a.service.ApplySurfaceAction(control.Action{
		Kind: control.ActionReactionCreated, SurfaceSessionID: memorySurface,
		ActorUserID: "bot", ReactionType: "OK", TargetMessageID: "message",
	})
	if got := a.service.SurfaceActorUserID(memorySurface); got != "owner" {
		t.Fatalf("passive reaction replaced reply owner: %q", got)
	}
	a.mu.Lock()
	allowed := a.workMemoryUIEventAllowedLocked(eventcontract.Event{
		SurfaceSessionID: memorySurface, Block: &render.Block{ThreadID: "thread", Text: "owner reply"},
	})
	a.mu.Unlock()
	if !allowed {
		t.Fatal("reaction silently blocked the private reply")
	}
}

func TestWorkMemoryChatEntryCoversNewOwnerTopicsOnly(t *testing.T) {
	for _, test := range []struct {
		name, surface, chat, actor string
		wantEntry                  bool
	}{
		{"new_topic", "feishu:app-1:chat:private-chat@root-new", "private-chat", "owner", true},
		{"other_actor", "feishu:app-1:chat:private-chat@root-new", "private-chat", "other", false},
		{"other_chat", "feishu:app-1:chat:shared@root-new", "shared", "owner", false},
		{"other_gateway", "feishu:app-2:chat:private-chat@root-new", "private-chat", "owner", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, c, root := memoryEntryFixture(t)
			memoryWrite(t, root, "manifest.json", map[string]any{
				"schema_version": 1, "enabled": true, "mode": "on_demand", "guide_path": "runtime-guide.json", "owner_user_id": "owner",
				"bindings": []any{map[string]any{"gateway_id": "app-1", "chat_id": "private-chat"}},
			})
			a.service.MaterializeSurface(test.surface, "app-1", test.chat, test.actor)
			s := a.service.Surface(test.surface)
			s.AttachedInstanceID, s.ActiveQueueItemID = "instance", "queue"
			s.QueueItems = a.service.Surface(memorySurface).QueueItems
			s.QueueItems["queue"].ActorUserID = test.actor
			c.Origin.Surface, c.Origin.ChatID, c.Origin.UserID = test.surface, test.chat, test.actor
			sent := memoryDispatch(t, a, c)
			if len(sent) != 1 {
				t.Fatal("ordinary request was blocked")
			}
			gotEntry := sent[0].CodexResume != nil && sent[0].CodexResume.DeveloperInstruction != "existing profile rule"
			if gotEntry != test.wantEntry {
				t.Fatalf("entry=%v, want %v", gotEntry, test.wantEntry)
			}
		})
	}
}
