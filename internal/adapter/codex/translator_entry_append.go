package codex

import (
	"encoding/json"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

type developerAppendState struct {
	Instruction string
	Epoch       int
}

type pendingDeveloperAppend struct {
	Command   agentproto.Command
	Thread    string
	NewThread bool
	Epoch     int
}

// A native append preserves inherited instructions without inventing a profile.
func (t *Translator) startPromptAfterEntry(thread string, command agentproto.Command, newThread bool) ([]byte, string, error) {
	entry := developerAppendState{Instruction: command.DeveloperInstructionAppend, Epoch: t.developerAppendEpoch[thread]}
	if entry.Instruction == "" || t.appliedDeveloperAppend[thread] == entry {
		return t.directTurnStart(thread, command, newThread)
	}
	request := t.NextRequest("entry-append")
	payload := map[string]any{"id": request, "method": "thread/inject_items", "params": map[string]any{
		"threadId": thread,
		"items":    []any{map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": entry.Instruction}}}},
	}}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	t.pendingDeveloperAppend[request] = pendingDeveloperAppend{Command: command, Thread: thread, NewThread: newThread, Epoch: entry.Epoch}
	return append(b, '\n'), request, nil
}

func (t *Translator) observeDeveloperAppendResponse(request string, pending pendingDeveloperAppend, message map[string]any) (Result, error) {
	delete(t.pendingDeveloperAppend, request)
	_, hasError := message["error"]
	_, hasResult := message["result"].(map[string]any)
	if hasError || !hasResult {
		return Result{Suppress: true, Events: []agentproto.Event{{
			Kind: agentproto.EventTurnCompleted, CommandID: pending.Command.CommandID, ThreadID: pending.Thread,
			Status: "failed", ErrorMessage: "Codex 拒绝了项目档案短入口，用户消息尚未开始执行。",
			TurnCompletionOrigin: agentproto.TurnCompletionOriginTurnStartRejected,
			Initiator:            agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: choose(pending.Command.Origin.Surface, pending.Command.Origin.ChatID)},
		}}}, nil
	}
	t.appliedDeveloperAppend[pending.Thread] = developerAppendState{Instruction: pending.Command.DeveloperInstructionAppend, Epoch: pending.Epoch}
	frame, _, err := t.startPromptAfterEntry(pending.Thread, pending.Command, pending.NewThread)
	if err != nil {
		return Result{}, err
	}
	return Result{Suppress: true, OutboundToCodex: [][]byte{frame}}, nil
}

func (t *Translator) noteDeveloperAppendCompaction(thread, turn, item string) {
	if thread == "" || turn == "" || item == "" {
		return
	}
	key := thread + "\x00" + turn + "\x00" + item
	if !t.seenDeveloperAppendCompactions[key] {
		t.seenDeveloperAppendCompactions[key] = true
		t.developerAppendEpoch[thread]++
	}
}
