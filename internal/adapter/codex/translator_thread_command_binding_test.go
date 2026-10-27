package codex

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"testing"
)

func TestCreatedThreadBindingUsesExactNativeRequestID(t *testing.T) {
	tr := NewTranslator("inst")
	for _, suffix := range []string{"a", "b"} {
		tr.pendingThreadCreate["request-"+suffix] = pendingThreadCreate{Command: agentproto.Command{
			CommandID: "command-" + suffix, Kind: agentproto.CommandPromptSend,
			Origin: agentproto.Origin{Surface: "same-surface"},
		}}
	}
	for _, suffix := range []string{"b", "a"} {
		r, err := tr.ObserveServer([]byte(`{"id":"request-` + suffix + `","result":{"thread":{"id":"thread-` + suffix + `","cwd":"/tmp"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range r.Events {
			if event.Kind == agentproto.EventThreadDiscovered {
				found = event.CommandID == "command-"+suffix && event.ThreadID == "thread-"+suffix && event.Initiator.SurfaceSessionID == "same-surface"
			}
		}
		if !found {
			t.Fatalf("missing exact binding for response %s: %+v", suffix, r.Events)
		}
		if len(r.OutboundToCodex) != 1 {
			t.Fatal("native turn start was lost")
		}
	}
}
