package daemon

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"testing"
	"time"
)

func TestWorkMemoryBlockedOptionalIOHonorsProductionBudget(t *testing.T) {
	runtime := &workMemoryRuntime{busy: make(chan struct{}, 1)}
	if runtime.ioBudget() != 200*time.Millisecond {
		t.Fatal("production I/O budget is not 200ms")
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	req := workMemoryRequest{Command: agentproto.Command{Target: agentproto.Target{ThreadID: "thread"}}}
	start := time.Now()
	result := runtime.prepareWithIO(req, func(privacyChecked func()) workMemoryResult {
		privacyChecked()
		<-release
		defer close(finished)
		return workMemoryResult{Text: "late optional context"}
	})
	elapsed := time.Since(start)
	close(release)
	<-finished
	runtime.busy <- struct{}{}
	<-runtime.busy
	if result.Err != nil || result.Text != "" {
		t.Fatal("timed-out optional snapshot changed original prompt")
	}
	if elapsed < 180*time.Millisecond || elapsed > time.Second {
		t.Fatalf("blocked I/O escaped bounded wait: %s", elapsed)
	}
}
