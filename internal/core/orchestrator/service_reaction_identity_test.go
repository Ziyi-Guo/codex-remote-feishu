package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestReactionCommandsCannotBorrowReplyOwner(t *testing.T) {
	for _, reaction := range []string{"ThumbsUp", "APPLAUSE"} {
		for _, actors := range []struct {
			name, reactor, queued string
			allowed               bool
		}{
			{"other_reactor", "other", "other", false},
			{"owner_foreign_input", "user-1", "other", false},
			{"owner_own_input", "user-1", "user-1", true},
		} {
			t.Run(reaction+"/"+actors.name, func(t *testing.T) {
				svc, surface := applauseFixture(t)
				surface.QueueItems["queue-1"] = &state.QueueItemRecord{
					ID: "queue-1", ActorUserID: actors.queued, SourceMessageID: "msg-text",
					Inputs:             []agentproto.Input{{Type: agentproto.InputText, Text: "queued text"}},
					FrozenDispatchPlan: agentproto.PromptDispatchPlan{ExecutionThreadID: "thread-1"}, Status: state.QueueItemQueued,
				}
				surface.QueuedQueueItemIDs = []string{"queue-1"}
				events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionReactionCreated,
					SurfaceSessionID: "surface-1", ActorUserID: actors.reactor, ReactionType: reaction, TargetMessageID: "msg-text"})
				command := false
				for _, event := range events {
					command = command || event.Command != nil
				}
				if command != actors.allowed {
					t.Fatalf("command=%v, want %v", command, actors.allowed)
				}
				if surface.ActorUserID != "user-1" {
					t.Fatal("reaction changed reply owner")
				}
				if !actors.allowed && surface.QueueItems["queue-1"].Status != state.QueueItemQueued {
					t.Fatal("rejected reaction consumed queued input")
				}
			})
		}
	}
}
