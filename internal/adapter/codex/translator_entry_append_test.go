package codex

import (
	"encoding/json"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func nativeEntryCommand(t *testing.T, thread string) agentproto.Command {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"commandId": "entry-command", "kind": "prompt.send", "origin": map[string]any{"surface": "surface"}, "target": map[string]any{"threadId": thread}, "prompt": map[string]any{"inputs": []any{map[string]any{"type": "text", "text": "original input"}}}, "developerInstructionAppend": "short entry"})
	var c agentproto.Command
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNativeDefaultEntryAppendsBeforeTurnAndAgainAfterCompaction(t *testing.T) {
	tr := NewTranslator("inst")
	tr.currentThreadID = "thread"
	c := nativeEntryCommand(t, "thread")
	if c.CodexResume != nil {
		t.Fatal("this must exercise the nil-policy native path")
	}
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	params := payloadParams(t, p, "thread/inject_items")
	if params["threadId"] != "thread" {
		t.Fatal(params)
	}
	item := params["items"].([]any)[0].(map[string]any)
	if item["role"] != "developer" {
		t.Fatal(item)
	}
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{}})
	r, err := tr.ObserveServer(response)
	if err != nil || !r.Suppress || len(r.OutboundToCodex) != 1 {
		t.Fatalf("append ack: %+v %v", r, err)
	}
	params = payloadParams(t, decodeSinglePayload(t, r.OutboundToCodex), "turn/start")
	if len(params["input"].([]any)) != 1 {
		t.Fatal(params)
	}
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "turn/start")
	if _, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-id"}}}`)); err != nil {
		t.Fatal(err)
	}
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p = decodeSinglePayload(t, frames)
	payloadParams(t, p, "thread/inject_items")
	if _, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-id"}}}`)); err != nil {
		t.Fatal(err)
	}
	response, _ = json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{}})
	r, err = tr.ObserveServer(response)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, r.OutboundToCodex), "turn/start")
}

func TestNativeDefaultEntryDoesNotTreatAnOlderCompactionEpochAsCurrent(t *testing.T) {
	tr := NewTranslator("inst")
	tr.currentThreadID = "thread"
	frames, err := tr.TranslateCommand(nativeEntryCommand(t, "thread"))
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	if _, err := tr.ObserveServer([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-id"}}}`)); err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{}})
	r, err := tr.ObserveServer(response)
	if err != nil {
		t.Fatal(err)
	}
	p = decodeSinglePayload(t, r.OutboundToCodex)
	payloadParams(t, p, "thread/inject_items")
	response, _ = json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{}})
	r, err = tr.ObserveServer(response)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, r.OutboundToCodex), "turn/start")
}

func TestNativeDefaultEntryKeepsStartTemplateAndWaitsForExactAppendAck(t *testing.T) {
	tr := NewTranslator("inst")
	if _, err := tr.ObserveClient([]byte(`{"method":"thread/start","params":{"developerInstructions":"existing native base","modelProvider":"native-provider"}}`)); err != nil {
		t.Fatal(err)
	}
	c := nativeEntryCommand(t, "")
	frames, err := tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodeSinglePayload(t, frames)
	params := payloadParams(t, p, "thread/start")
	if params["developerInstructions"] != "existing native base" || params["modelProvider"] != "native-provider" {
		t.Fatal(params)
	}
	response, _ := json.Marshal(map[string]any{"id": p["id"], "result": map[string]any{"thread": map[string]any{"id": "new-thread"}}})
	r, err := tr.ObserveServer(response)
	if err != nil || len(r.OutboundToCodex) != 1 {
		t.Fatalf("start: %+v %v", r, err)
	}
	p = decodeSinglePayload(t, r.OutboundToCodex)
	payloadParams(t, p, "thread/inject_items")
	unknown, err := tr.ObserveServer([]byte(`{"id":"unrelated","result":{}}`))
	if err != nil || len(unknown.OutboundToCodex) != 0 {
		t.Fatal(unknown, err)
	}
	response, _ = json.Marshal(map[string]any{"id": p["id"], "error": map[string]any{"message": "rejected"}})
	r, err = tr.ObserveServer(response)
	if err != nil || len(r.OutboundToCodex) != 0 || !r.Suppress {
		t.Fatalf("rejected append: %+v %v", r, err)
	}
	c.Target.ThreadID = "new-thread"
	frames, err = tr.TranslateCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	payloadParams(t, decodeSinglePayload(t, frames), "thread/inject_items")
}
