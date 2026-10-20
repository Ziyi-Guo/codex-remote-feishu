package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func readOnlyTranslatorPolicy(t *testing.T) *limitedgroups.ExecutionPolicy {
	t.Helper()
	p := limitedTranslatorPolicy(t)
	p.ExecutionMode = limitedgroups.ReadOnlyNetworkNever
	if err := os.MkdirAll(p.Workdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := limitedgroups.EnsureForbiddenRules(p.Workdir); err != nil {
		t.Fatal(err)
	}
	var err error
	p.RulesSHA256, err = limitedgroups.ForbiddenRulesSHA256(p.Workdir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLimitedReadOnlyNativeEvidence(t *testing.T) {
	for _, bad := range []string{"", "approval", "network", "filesystem", "cwd", "profile"} {
		t.Run(bad, func(t *testing.T) {
			p := readOnlyTranslatorPolicy(t)
			tr := NewTranslator("limited")
			frames, err := tr.TranslateCommand(limitedTranslatorPrompt(p))
			if err != nil {
				t.Fatal(err)
			}
			req := decodePayload(t, frames[0])
			params := lookupMap(req, "params")
			if params["approvalPolicy"] != "never" || !strings.Contains(lookupString(params, "developerInstructions"), "不请求郭") {
				t.Fatal("read-only instructions/policy missing")
			}
			raw := limitedTranslatorResponse(t, req["id"], "readonly-thread", p, func(r map[string]any) {
				r["approvalPolicy"] = "never"
				r["sandbox"] = map[string]any{"type": "readOnly", "networkAccess": true}
				switch bad {
				case "approval":
					r["approvalPolicy"] = "on-request"
				case "network":
					lookupMap(r, "sandbox")["networkAccess"] = false
				case "filesystem":
					lookupMap(r, "sandbox")["type"] = "workspaceWrite"
				case "cwd":
					r["cwd"] = "/other"
				case "profile":
					r["activePermissionProfile"] = map[string]any{"id": "other"}
				}
			})
			result, err := tr.ObserveServer(raw)
			if err != nil {
				t.Fatal(err)
			}
			if bad == "" && len(result.OutboundToCodex) == 0 {
				t.Fatal("valid read-only evidence did not start turn")
			}
			if bad == "" {
				turn := lookupMap(decodePayload(t, result.OutboundToCodex[0]), "params")
				if turn["approvalPolicy"] != "never" || turn["sandboxPolicy"] != nil {
					t.Fatal("turn replaced verified never policy")
				}
			}
			if bad != "" && len(result.OutboundToCodex) != 0 {
				t.Fatal("mismatching evidence started a turn")
			}
		})
	}
}

func TestLimitedNeverAuthorizationDoesNotReachUI(t *testing.T) {
	p := readOnlyTranslatorPolicy(t)
	tr := NewTranslator("limited")
	if _, err := tr.TranslateCommand(limitedTranslatorPrompt(p)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request", "execCommandApproval", "applyPatchApproval"} {
		raw, _ := json.Marshal(map[string]any{"id": 37, "method": method, "params": map[string]any{"threadId": "readonly-thread", "command": "bytedcli rds db list"}})
		result, err := tr.ObserveServer(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Suppress || len(result.Events) != 0 || len(result.OutboundToCodex) != 1 {
			t.Fatalf("authorization leaked to UI: %s %#v", method, result)
		}
		response := decodePayload(t, result.OutboundToCodex[0])
		if response["id"] != float64(37) {
			t.Fatal("native request ID changed")
		}
		payload := lookupMap(response, "result")
		if payload["decision"] == "accept" || payload["action"] == "accept" {
			t.Fatal("authorization accepted")
		}
	}
	result, err := tr.ObserveServer([]byte(`{"id":38,"method":"item/tool/requestUserInput","params":{"threadId":"readonly-thread","questions":[{"id":"q","header":"选择","question":"哪个项目？","options":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) == 0 {
		t.Fatal("ordinary user question suppressed")
	}
	personal := NewTranslator("personal")
	result, err = personal.ObserveServer([]byte(`{"id":39,"method":"item/commandExecution/requestApproval","params":{"threadId":"personal","command":"touch x"}}`))
	if err != nil || len(result.Events) == 0 {
		t.Fatal("personal approval path changed")
	}
}
