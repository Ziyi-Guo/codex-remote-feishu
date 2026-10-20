package codex

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func (t *Translator) guardLimitedCommand(c agentproto.Command) error {
	p := c.Overrides.LimitedExecution
	if t.limitedExecution == nil && p == nil {
		return nil
	}
	if p == nil || p.Validate() != nil {
		return fmt.Errorf("limited execution policy required")
	}
	if t.limitedExecution != nil && !reflect.DeepEqual(t.limitedExecution, p) {
		return fmt.Errorf("limited execution process cannot change authority")
	}
	switch c.Kind {
	case agentproto.CommandPromptSend:
		if c.Target.CWD != p.Workdir || c.Target.EffectivePromptExecutionMode() == agentproto.PromptExecutionModeForkEphemeral {
			return fmt.Errorf("limited execution workspace is pinned")
		}
	case agentproto.CommandTurnSteer, agentproto.CommandTurnInterrupt:
	case agentproto.CommandRequestRespond:
		typ, ok := t.pendingRequestTypes[c.Request.RequestID]
		if !ok {
			return fmt.Errorf("limited response has no native pending request")
		}
		if typ != agentproto.RequestTypeRequestUserInput {
			if c.Origin.UserID != p.OwnerUserID || !limitedSingleUseResponse(c.Request.Response) {
				return fmt.Errorf("limited operation requires owner single-use approval")
			}
		}
	default:
		return fmt.Errorf("command is unavailable in limited execution")
	}
	if t.limitedExecution == nil {
		raw, _ := json.Marshal(p)
		var frozen limitedgroups.ExecutionPolicy
		if err := json.Unmarshal(raw, &frozen); err != nil {
			return err
		}
		t.limitedExecution = &frozen
	}
	return nil
}

func limitedSingleUseResponse(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "permissions" {
				if a, ok := v.([]any); !ok || len(a) != 0 {
					return false
				}
				continue
			}
			if k == "persist" || k == "execPolicyAmendment" || k == "networkPolicyAmendments" {
				return false
			}
			if k == "decision" && v != "accept" && v != "decline" && v != "cancel" {
				return false
			}
			if k == "scope" && v != "turn" {
				return false
			}
			if !limitedSingleUseResponse(v) {
				return false
			}
		}
	case []any:
		for _, v := range x {
			if !limitedSingleUseResponse(v) {
				return false
			}
		}
	}
	return true
}

func applyLimitedThreadParams(params map[string]any, p *limitedgroups.ExecutionPolicy) {
	if p == nil {
		return
	}
	params["cwd"] = p.Workdir
	params["approvalPolicy"] = "on-request"
	params["approvalsReviewer"] = "user"
	delete(params, "sandbox")
	delete(params, "sandboxPolicy")
	c := lookupMapFromAny(params["config"])
	delete(c, "sandbox")
	delete(c, "sandbox_mode")
	for k, v := range p.ConfigOverrides() {
		c[k] = v
	}
	params["config"] = c
}

func (t *Translator) verifyLimitedResponse(message map[string]any, p *limitedgroups.ExecutionPolicy, threadID string) error {
	if p == nil {
		return nil
	}
	r := lookupMap(message, "result")
	sandbox := lookupMap(r, "sandbox")
	if r["approvalPolicy"] != "on-request" || r["approvalsReviewer"] != "user" || lookupString(r, "activePermissionProfile", "id") != "profc_limited" || sandbox["type"] != "workspaceWrite" || sandbox["networkAccess"] != false || sandbox["excludeTmpdirEnvVar"] != true || sandbox["excludeSlashTmp"] != true {
		return fmt.Errorf("native limited permission evidence mismatch")
	}
	roots, ok := sandbox["writableRoots"].([]any)
	if !ok || len(roots) != 1 {
		return fmt.Errorf("native limited write roots mismatch")
	}
	root, ok := roots[0].(string)
	if !ok || canonicalLimitedPath(root) != canonicalLimitedPath(p.OutputDir) || canonicalLimitedPath(lookupString(r, "cwd")) != canonicalLimitedPath(p.Workdir) {
		return fmt.Errorf("native limited roots mismatch")
	}
	t.limitedVerifiedThread = threadID
	return nil
}

func canonicalLimitedPath(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

func limitedNativePermissionFailure(command agentproto.Command, threadID string, err error) Result {
	return Result{Suppress: true, Events: []agentproto.Event{{Kind: agentproto.EventTurnCompleted, CommandID: command.CommandID, ThreadID: threadID, Status: "failed", ErrorMessage: err.Error(), TurnCompletionOrigin: agentproto.TurnCompletionOriginThreadStartRejected, Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: command.Origin.Surface}}}}
}

func (t *Translator) autoApproveLimitedRead(message map[string]any) (Result, bool) {
	if t.limitedExecution == nil || t.limitedExecution.Validate() != nil || message["method"] != "item/commandExecution/requestApproval" {
		return Result{}, false
	}
	params := lookupMap(message, "params")
	if t.limitedVerifiedThread == "" || lookupString(params, "threadId") != t.limitedVerifiedThread || len(lookupMap(params, "networkApprovalContext")) != 0 {
		return Result{}, false
	}
	command, ok := params["command"].(string)
	if !ok || !limitedgroups.IsKnownReadApprovalCommand(command) {
		return Result{}, false
	}
	id, ok := message["id"]
	if !ok {
		return Result{}, false
	}
	raw, err := json.Marshal(map[string]any{"id": id, "result": map[string]any{"decision": "accept"}})
	if err != nil {
		return Result{}, false
	}
	t.Debugf("limited read single-use approval: request=%v thread=%s", id, t.limitedVerifiedThread)
	return Result{Suppress: true, OutboundToCodex: [][]byte{append(raw, '\n')}}, true
}
