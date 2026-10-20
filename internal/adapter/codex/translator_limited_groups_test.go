package codex

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func limitedTranslatorPolicy(t *testing.T) *limitedgroups.ExecutionPolicy {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &limitedgroups.ExecutionPolicy{
		OwnerUserID: "ou_owner", Workspace: filepath.Join(root, "source"),
		Workdir: filepath.Join(root, "limited"), OutputDir: filepath.Join(root, "limited", "output"),
		Servers: []limitedgroups.ToolServer{{Name: "test-tools", Tools: []string{"read", "write"}}},
	}
}

func limitedTranslatorPrompt(policy *limitedgroups.ExecutionPolicy) agentproto.Command {
	return agentproto.Command{
		Kind:      agentproto.CommandPromptSend,
		Origin:    agentproto.Origin{ChatID: "limited-group"},
		Target:    agentproto.Target{CWD: policy.Workdir, ExecutionMode: agentproto.PromptExecutionModeStartNew},
		Prompt:    agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "read the source"}}},
		Overrides: agentproto.PromptOverrides{AccessMode: agentproto.AccessModeFullAccess, LimitedExecution: policy},
	}
}

func assertLimitedThreadParams(t *testing.T, params map[string]any, policy *limitedgroups.ExecutionPolicy) {
	t.Helper()
	if params["cwd"] != policy.Workdir || params["approvalPolicy"] != "on-request" || params["approvalsReviewer"] != "user" {
		t.Fatalf("limited thread must override personal cwd/approval/reviewer: %#v", params)
	}
	for _, key := range []string{"sandbox", "sandboxPolicy"} {
		if value := params[key]; value != nil {
			t.Fatalf("legacy %s must not override named permissions: %#v", key, value)
		}
	}
	config := payloadConfig(t, params)
	// JSON round-trip aligns map and integer representations with the wire payload.
	raw, err := json.Marshal(policy.ConfigOverrides())
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if !reflect.DeepEqual(config[key], value) {
			t.Fatalf("missing limited config %q: got %#v want %#v", key, config[key], value)
		}
	}
	for _, key := range []string{"sandbox", "sandbox_mode"} {
		if value := config[key]; value != nil {
			t.Fatalf("inherited legacy config %s overrides permissions: %#v", key, value)
		}
	}
}

func assertLimitedTurnParams(t *testing.T, params map[string]any, policy *limitedgroups.ExecutionPolicy) {
	t.Helper()
	if params["cwd"] != policy.Workdir || params["approvalPolicy"] != "on-request" || params["approvalsReviewer"] != "user" {
		t.Fatalf("limited turn must preserve workdir/user approvals: %#v", params)
	}
	for _, key := range []string{"sandbox", "sandboxPolicy"} {
		if params[key] != nil {
			t.Fatalf("turn %s must not replace named permissions: %#v", key, params[key])
		}
	}
}

// Shape follows the native 0.160.1 output-write preflight: the legacy projection
// is workspaceWrite with exactly the canonical output root and no ambient tmp writes.
func limitedTranslatorResponse(t *testing.T, requestID any, threadID string, policy *limitedgroups.ExecutionPolicy, mutate func(map[string]any)) []byte {
	t.Helper()
	result := map[string]any{
		"thread": map[string]any{"id": threadID, "cwd": policy.Workdir}, "cwd": policy.Workdir,
		"approvalPolicy": "on-request", "approvalsReviewer": "user",
		"sandbox": map[string]any{
			"type": "workspaceWrite", "writableRoots": []string{policy.OutputDir},
			"networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true,
		},
		"activePermissionProfile": map[string]any{"id": "profc_limited"},
	}
	if mutate != nil {
		mutate(result)
	}
	raw, err := json.Marshal(map[string]any{"id": requestID, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLimitedGroupsTranslatorStartAndTurnOverridePersonalFullAccess(t *testing.T) {
	policy := limitedTranslatorPolicy(t)
	tr := NewTranslator("limited")
	// A privileged personal template must never win over the daemon's guard.
	if _, err := tr.ObserveClient([]byte(`{"method":"thread/start","params":{"cwd":"/tmp/personal","approvalPolicy":"never","approvalsReviewer":"auto_review","sandbox":"danger-full-access","config":{"approval_policy":"never","approvals_reviewer":"auto_review","sandbox_mode":"danger-full-access"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"personal","approvalPolicy":"never","approvalsReviewer":"auto_review","sandboxPolicy":{"type":"dangerFullAccess"}}}`)); err != nil {
		t.Fatal(err)
	}
	frames, err := tr.TranslateCommand(limitedTranslatorPrompt(policy))
	if err != nil {
		t.Fatal(err)
	}
	start := decodeSinglePayload(t, frames)
	assertLimitedThreadParams(t, payloadParams(t, start, "thread/start"), policy)
	observed, err := tr.ObserveServer(limitedTranslatorResponse(t, start["id"], "limited-thread", policy, nil))
	if err != nil {
		t.Fatal(err)
	}
	assertLimitedTurnParams(t, payloadParams(t, decodeSinglePayload(t, observed.OutboundToCodex), "turn/start"), policy)
	// Subsequent turns carry the guard again and cannot revive an inherited
	// dangerFullAccess turn template or the explicit personal AccessMode above.
	next := limitedTranslatorPrompt(policy)
	next.Target = agentproto.Target{ThreadID: "limited-thread", CWD: policy.Workdir}
	frames, err = tr.TranslateCommand(next)
	if err != nil {
		t.Fatal(err)
	}
	assertLimitedTurnParams(t, payloadParams(t, decodeSinglePayload(t, frames), "turn/start"), policy)
}

func TestLimitedGroupsTranslatorResumeReappliesGuardBeforeTurn(t *testing.T) {
	policy := limitedTranslatorPolicy(t)
	tr := NewTranslator("limited")
	command := limitedTranslatorPrompt(policy)
	command.Target = agentproto.Target{ThreadID: "existing-limited", CWD: policy.Workdir}
	frames, err := tr.TranslateCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	resume := decodeSinglePayload(t, frames)
	assertLimitedThreadParams(t, payloadParams(t, resume, "thread/resume"), policy)
	observed, err := tr.ObserveServer(limitedTranslatorResponse(t, resume["id"], "existing-limited", policy, nil))
	if err != nil {
		t.Fatal(err)
	}
	assertLimitedTurnParams(t, payloadParams(t, decodeSinglePayload(t, observed.OutboundToCodex), "turn/start"), policy)
}

func TestLimitedGroupsTranslatorRejectsUnverifiedNativePermissions(t *testing.T) {
	tests := map[string]func(map[string]any){
		"missing approval":    func(r map[string]any) { delete(r, "approvalPolicy") },
		"never approval":      func(r map[string]any) { r["approvalPolicy"] = "never" },
		"missing reviewer":    func(r map[string]any) { delete(r, "approvalsReviewer") },
		"automatic reviewer":  func(r map[string]any) { r["approvalsReviewer"] = "auto_review" },
		"missing sandbox":     func(r map[string]any) { delete(r, "sandbox") },
		"full access sandbox": func(r map[string]any) { r["sandbox"] = map[string]any{"type": "dangerFullAccess"} },
		"read only sandbox": func(r map[string]any) {
			r["sandbox"] = map[string]any{"type": "readOnly", "networkAccess": false}
		},
		"missing write roots": func(r map[string]any) { delete(r["sandbox"].(map[string]any), "writableRoots") },
		"arbitrary write root": func(r map[string]any) {
			r["sandbox"].(map[string]any)["writableRoots"] = []string{filepath.Dir(r["cwd"].(string))}
		},
		"workdir writable": func(r map[string]any) {
			r["sandbox"].(map[string]any)["writableRoots"] = []string{r["cwd"].(string)}
		},
		"source writable": func(r map[string]any) {
			r["sandbox"].(map[string]any)["writableRoots"] = []string{filepath.Join(filepath.Dir(r["cwd"].(string)), "source")}
		},
		"extra workdir writable": func(r map[string]any) {
			sandbox := r["sandbox"].(map[string]any)
			sandbox["writableRoots"] = append(sandbox["writableRoots"].([]string), r["cwd"].(string))
		},
		"extra source writable": func(r map[string]any) {
			sandbox := r["sandbox"].(map[string]any)
			sandbox["writableRoots"] = append(sandbox["writableRoots"].([]string), filepath.Join(filepath.Dir(r["cwd"].(string)), "source"))
		},
		"network allowed":             func(r map[string]any) { r["sandbox"].(map[string]any)["networkAccess"] = true },
		"network unverified":          func(r map[string]any) { delete(r["sandbox"].(map[string]any), "networkAccess") },
		"missing tmpdir exclusion":    func(r map[string]any) { delete(r["sandbox"].(map[string]any), "excludeTmpdirEnvVar") },
		"tmpdir writable":             func(r map[string]any) { r["sandbox"].(map[string]any)["excludeTmpdirEnvVar"] = false },
		"missing slash tmp exclusion": func(r map[string]any) { delete(r["sandbox"].(map[string]any), "excludeSlashTmp") },
		"slash tmp writable":          func(r map[string]any) { r["sandbox"].(map[string]any)["excludeSlashTmp"] = false },
		"missing profile":             func(r map[string]any) { delete(r, "activePermissionProfile") },
		"empty profile":               func(r map[string]any) { r["activePermissionProfile"] = map[string]any{"id": ""} },
		"null profile":                func(r map[string]any) { r["activePermissionProfile"] = nil },
		"wrong profile":               func(r map[string]any) { r["activePermissionProfile"] = map[string]any{"id": ":workspace"} },
	}
	for _, resume := range []bool{false, true} {
		for name, mutate := range tests {
			t.Run(name+map[bool]string{false: "/start", true: "/resume"}[resume], func(t *testing.T) {
				policy := limitedTranslatorPolicy(t)
				tr := NewTranslator("limited")
				command := limitedTranslatorPrompt(policy)
				if resume {
					command.Target = agentproto.Target{ThreadID: "existing-limited", CWD: policy.Workdir}
				}
				frames, err := tr.TranslateCommand(command)
				if err != nil {
					t.Fatal(err)
				}
				request := decodeSinglePayload(t, frames)
				threadID := "limited-thread"
				if resume {
					threadID = "existing-limited"
				}
				result, _ := tr.ObserveServer(limitedTranslatorResponse(t, request["id"], threadID, policy, mutate))
				if len(result.OutboundToCodex) != 0 {
					t.Fatalf("unverified permission response sent %d native frames", len(result.OutboundToCodex))
				}
			})
		}
	}
}

func TestLimitedGroupsTranslatorPinsProcessGuard(t *testing.T) {
	for _, scenario := range []string{"no guard", "other cwd", "other workspace", "other owner", "other output", "other workdir", "other servers"} {
		t.Run(scenario, func(t *testing.T) {
			policy := limitedTranslatorPolicy(t)
			tr := NewTranslator("limited")
			frames, err := tr.TranslateCommand(limitedTranslatorPrompt(policy))
			if err != nil {
				t.Fatal(err)
			}
			request := decodeSinglePayload(t, frames)
			if _, err := tr.ObserveServer(limitedTranslatorResponse(t, request["id"], "limited-thread", policy, nil)); err != nil {
				t.Fatal(err)
			}
			next := limitedTranslatorPrompt(policy)
			next.Target = agentproto.Target{ThreadID: "limited-thread", CWD: policy.Workdir}
			changed := *policy
			switch scenario {
			case "no guard":
				next.Overrides.LimitedExecution = nil
			case "other cwd":
				next.Target.CWD = filepath.Join(t.TempDir(), "personal")
			case "other workspace":
				changed.Workspace = filepath.Join(t.TempDir(), "source")
				next.Overrides.LimitedExecution = &changed
			case "other owner":
				changed.OwnerUserID = "ou_other"
				next.Overrides.LimitedExecution = &changed
			case "other workdir":
				changed.Workdir = filepath.Join(t.TempDir(), "another-limited")
				changed.OutputDir = filepath.Join(changed.Workdir, "output")
				next.Target.CWD = changed.Workdir
				next.Overrides.LimitedExecution = &changed
			case "other servers":
				changed.Servers = []limitedgroups.ToolServer{{Name: "different-tools", Tools: []string{"read", "write"}}}
				next.Overrides.LimitedExecution = &changed
			case "other output":
				changed.OutputDir = filepath.Join(policy.Workdir, "other-output")
				next.Overrides.LimitedExecution = &changed
			}
			frames, err = tr.TranslateCommand(next)
			if err == nil || len(frames) != 0 {
				t.Fatalf("process guard escape %q was accepted: frames=%d err=%v", scenario, len(frames), err)
			}
		})
	}
}

func TestLimitedGroupsTranslatorRejectsSideDoorCommands(t *testing.T) {
	for _, scenario := range []string{"compact", "review", "fork"} {
		t.Run(scenario, func(t *testing.T) {
			policy := limitedTranslatorPolicy(t)
			tr := NewTranslator("limited")
			if _, err := tr.TranslateCommand(limitedTranslatorPrompt(policy)); err != nil {
				t.Fatal(err)
			}
			command := limitedTranslatorPrompt(policy)
			command.Target.ThreadID = "limited-thread"
			switch scenario {
			case "compact":
				command.Kind = agentproto.CommandThreadCompactStart
			case "review":
				command.Kind = agentproto.CommandReviewStart
				command.Review = agentproto.ReviewRequest{Delivery: agentproto.ReviewDeliveryDetached, Target: agentproto.ReviewTarget{Kind: agentproto.ReviewTargetKindUncommittedChanges}}
			case "fork":
				command.Target.ExecutionMode = agentproto.PromptExecutionModeForkEphemeral
				command.Target.SourceThreadID = "limited-thread"
			}
			frames, err := tr.TranslateCommand(command)
			if err == nil || len(frames) != 0 {
				t.Fatalf("unsupported limited command %q escaped: frames=%d err=%v", scenario, len(frames), err)
			}
		})
	}
}

func TestLimitedGroupsTranslatorNewChildResumeMustReapplyGuard(t *testing.T) {
	// A restarted wrapper has no in-memory pin. The daemon must send the complete
	// guard with every prompt, including the first resume in the new process.
	policy := limitedTranslatorPolicy(t)
	tr := NewTranslator("restarted-limited")
	command := limitedTranslatorPrompt(policy)
	command.Target = agentproto.Target{ThreadID: "limited-before-restart", CWD: policy.Workdir}
	frames, err := tr.TranslateCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	assertLimitedThreadParams(t, payloadParams(t, decodeSinglePayload(t, frames), "thread/resume"), policy)
}
