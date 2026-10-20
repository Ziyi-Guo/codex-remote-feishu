package daemon

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
	"testing"
	"time"
)

func TestLimitedGroupsStartupPreservesAllQuestions(t *testing.T) {
	a := New(":0", ":0", nil, agentproto.ServerIdentity{})
	now := time.Now()
	for _, id := range []string{"first", "second", "third"} {
		a.saveGroupOnDemandResumeContinuationLocked("limited", control.Action{MessageID: id, LimitedExecution: &limitedgroups.ExecutionPolicy{}}, now)
	}
	pending, ok := a.consumeGroupOnDemandResumeContinuationLocked("limited")
	if !ok || pending.Action.MessageID != "first" || len(pending.Additional) != 2 || pending.Additional[0].MessageID != "second" || pending.Additional[1].MessageID != "third" {
		t.Fatalf("startup silently lost or reordered a question: %#v", pending)
	}
}
