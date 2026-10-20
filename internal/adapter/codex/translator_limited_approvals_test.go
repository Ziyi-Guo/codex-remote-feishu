package codex

import (
	"encoding/json"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"testing"
)

func TestLimitedGroupsAutoApprovesOnlyCompleteReadCommand(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		safe bool
	}{
		{`/bin/zsh -lc 'bytedcli --json rds db table schema ip_fun content --region cn'`, true},
		{`/bin/bash -c 'bytedcli rds db query ip_fun "SELECT name FROM content LIMIT 1" --region cn'`, true},
		{`/bin/bash -c 'bytedcli rds db list; touch /tmp/pwn'`, false},
		{`curl -X POST http://localhost/write`, false},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			p := limitedTranslatorPolicy(t)
			tr := NewTranslator("limited")
			frames, err := tr.TranslateCommand(limitedTranslatorPrompt(p))
			if err != nil {
				t.Fatal(err)
			}
			req := decodeSinglePayload(t, frames)
			if _, err = tr.ObserveServer(limitedTranslatorResponse(t, req["id"], "limited-thread", p, nil)); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"id": 71, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "limited-thread", "turnId": "turn", "command": tc.cmd, "commandActions": []any{map[string]any{"command": "bytedcli rds db list"}}}})
			r, err := tr.ObserveServer(raw)
			if err != nil {
				t.Fatal(err)
			}
			if tc.safe {
				if len(r.OutboundToCodex) != 1 || len(r.Events) != 0 {
					t.Fatalf("safe read was not accepted once: %#v", r)
				}
				v := decodeSinglePayload(t, r.OutboundToCodex)
				if v["id"] != float64(71) || lookupString(v, "result", "decision") != "accept" {
					t.Fatalf("not single-use native decision: %#v", v)
				}
			} else if len(r.OutboundToCodex) != 0 || len(r.Events) != 1 {
				t.Fatalf("write did not remain pending for owner: %#v", r)
			}
		})
	}
}

func TestLimitedGroupsWrapperRequiresRealOwnerAndOneResponse(t *testing.T) {
	p := limitedTranslatorPolicy(t)
	tr := NewTranslator("limited")
	if _, err := tr.TranslateCommand(limitedTranslatorPrompt(p)); err != nil {
		t.Fatal(err)
	}
	_, err := tr.ObserveServer([]byte(`{"id":"write-request","method":"item/commandExecution/requestApproval","params":{"threadId":"limited","command":"touch /tmp/x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{UserID: "ou_pan"}, Overrides: agentproto.PromptOverrides{LimitedExecution: p}, Request: agentproto.Request{RequestID: "write-request", Response: map[string]any{"type": "approval", "decision": "accept"}}}
	if f, err := tr.TranslateCommand(c); err == nil || len(f) != 0 {
		t.Fatal("guest approved its own write")
	}
	c.Origin.UserID = p.OwnerUserID
	if f, err := tr.TranslateCommand(c); err != nil || len(f) != 1 {
		t.Fatalf("owner single approval rejected: %v", err)
	}
	if f, err := tr.TranslateCommand(c); err == nil || len(f) != 0 {
		t.Fatal("old approval replayed")
	}
}

func TestLimitedGroupsStdinSameItemNeedsSeparateOwnerApproval(t *testing.T) {
	p := limitedTranslatorPolicy(t)
	tr := NewTranslator("limited")
	_, err := tr.TranslateCommand(limitedTranslatorPrompt(p))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(map[string]any{"id": i, "method": "item/commandExecution/requestApproval", "params": map[string]any{"kind": "writeStdin", "itemId": "call_start_bash", "approvalId": i, "threadId": "limited-thread", "command": "write_stdin --session-id 22097 'printf marker > source/file'", "availableDecisions": []string{"accept", "cancel"}}})
		r, err := tr.ObserveServer(raw)
		if err != nil || len(r.Events) != 1 || len(r.OutboundToCodex) != 0 {
			t.Fatalf("stdin approval bypass: %v", err)
		}
		id := r.Events[0].RequestID
		ids = append(ids, id)
		c := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{UserID: p.OwnerUserID}, Overrides: agentproto.PromptOverrides{LimitedExecution: p}, Request: agentproto.Request{RequestID: id, Response: map[string]any{"type": "approval", "decision": "cancel"}}}
		if _, err := tr.TranslateCommand(c); err != nil {
			t.Fatalf("owner cannot cancel stdin: %v", err)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct native request IDs collapsed onto item ID")
	}
}
