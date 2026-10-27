package codex

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
	"github.com/kxn/codex-remote-feishu/internal/pathcanon"
)

// Native request IDs bind start/resume responses to the dispatched command.
func (t *Translator) observePendingThreadResponse(requestID string, message map[string]any) (Result, error) {
	if pending, exists := t.pendingThreadCreate[requestID]; exists {
		delete(t.pendingThreadCreate, requestID)
		if errMsg := jsonrpcutil.ExtractErrorMessage(message); errMsg != "" {
			delete(t.pendingInternalThreadSet, requestID)
			action := choose(strings.TrimSpace(pending.Action), "thread/start")
			t.Debugf("observe server %s error: request=%s error=%s", action, requestID, errMsg)
			return Result{Events: []agentproto.Event{{
				Kind:                 agentproto.EventTurnCompleted,
				Status:               "failed",
				ErrorMessage:         errMsg,
				TurnCompletionOrigin: agentproto.TurnCompletionOriginThreadStartRejected,
			}}}, nil
		}
		threadID := lookupString(message, "result", "thread", "id")
		if threadID == "" {
			threadID = lookupString(message, "result", "id")
		}
		if t.pendingInternalThreadSet[requestID] {
			delete(t.pendingInternalThreadSet, requestID)
			if threadID != "" {
				t.internalThreadIDs[threadID] = true
			}
		}
		t.currentThreadID = threadID
		if err := t.verifyLimitedResponse(message, pending.Command.Overrides.LimitedExecution, threadID); err != nil {
			return limitedNativePermissionFailure(pending.Command, threadID, err), nil
		}
		if pending.Command.Target.CWD != "" {
			t.knownThreadCWD[threadID] = pathcanon.Native(pending.Command.Target.CWD)
		}
		t.noteDeveloperInstructionsApplied(threadID, pending.Command.CodexResume)
		followup, followupID, err := t.startPromptAfterEntry(threadID, pending.Command, true)
		if err != nil {
			return Result{}, err
		}
		action := choose(strings.TrimSpace(pending.Action), "thread/start")
		t.Debugf("observe server %s result: request=%s thread=%s followup=%s", action, requestID, threadID, followupID)
		return Result{
			Suppress:        true,
			Events:          []agentproto.Event{t.createdThreadCommandBinding(message, pending.Command, threadID)},
			OutboundToCodex: [][]byte{followup},
		}, nil
	}
	if t.pendingInternalThreadSet[requestID] {
		delete(t.pendingInternalThreadSet, requestID)
		threadID := lookupString(message, "result", "thread", "id")
		if threadID == "" {
			threadID = lookupString(message, "result", "id")
		}
		if threadID != "" {
			t.internalThreadIDs[threadID] = true
		}
		return Result{}, nil
	}
	if pending, exists := t.pendingThreadResume[requestID]; exists {
		delete(t.pendingThreadResume, requestID)
		if errMsg := jsonrpcutil.ExtractErrorMessage(message); errMsg != "" {
			t.Debugf("observe server thread/resume error: request=%s thread=%s kind=%s error=%s", requestID, pending.ThreadID, pending.Command.Kind, errMsg)
			if pending.Command.Kind == agentproto.CommandThreadCompactStart {
				return Result{Events: []agentproto.Event{agentproto.NewSystemErrorEvent(agentproto.ErrorInfo{
					Code:             "compact_start_failed",
					Layer:            "server",
					Stage:            "thread_resume_response",
					Operation:        "thread.compact.start",
					Message:          "Codex 拒绝了这次上下文压缩请求。",
					Details:          errMsg,
					SurfaceSessionID: choose(pending.Command.Origin.Surface, pending.Command.Origin.ChatID),
					ThreadID:         pending.ThreadID,
				})}}, nil
			}
			return Result{Events: []agentproto.Event{{
				Kind:                 agentproto.EventTurnCompleted,
				ThreadID:             pending.ThreadID,
				Status:               "failed",
				ErrorMessage:         errMsg,
				TurnCompletionOrigin: agentproto.TurnCompletionOriginThreadResumeRejected,
			}}}, nil
		}
		t.currentThreadID = pending.ThreadID
		if err := t.verifyLimitedResponse(message, pending.Command.Overrides.LimitedExecution, pending.ThreadID); err != nil {
			return limitedNativePermissionFailure(pending.Command, pending.ThreadID, err), nil
		}
		if pending.Command.Target.CWD != "" {
			t.knownThreadCWD[pending.ThreadID] = pathcanon.Native(pending.Command.Target.CWD)
		}
		t.noteDeveloperInstructionsApplied(pending.ThreadID, pending.Command.CodexResume)
		switch pending.Command.Kind {
		case agentproto.CommandThreadCompactStart:
			followup, followupID, err := t.directCompactStart(pending.Command)
			if err != nil {
				return Result{}, err
			}
			t.Debugf("observe server thread/resume result: request=%s thread=%s compactFollowup=%s", requestID, pending.ThreadID, followupID)
			return Result{
				Suppress:        true,
				OutboundToCodex: [][]byte{followup},
			}, nil
		default:
			followup, followupID, err := t.startPromptAfterEntry(pending.ThreadID, pending.Command, false)
			if err != nil {
				return Result{}, err
			}
			t.Debugf("observe server thread/resume result: request=%s thread=%s followup=%s", requestID, pending.ThreadID, followupID)
			return Result{
				Suppress:        true,
				OutboundToCodex: [][]byte{followup},
			}, nil
		}
	}

	return Result{}, nil
}
