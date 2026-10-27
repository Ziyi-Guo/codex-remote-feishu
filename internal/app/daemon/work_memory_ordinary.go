package daemon

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

// These proofs exist only in this lifecycle and originate in plain new commands,
// never in an actor match, a resumed thread, or a time-based guess.
func (r *workMemoryRuntime) noteOrdinaryDispatch(req workMemoryRequest, result workMemoryResult) {
	r.proofMu.Lock()
	defer r.proofMu.Unlock()
	if result.Text != "" {
		delete(r.ordinaryThreads, req.Command.Target.ThreadID)
		return
	}
	c := req.Command
	if result.Err != nil || !req.UserInput || c.Kind != agentproto.CommandPromptSend || c.Target.InternalHelper || c.Target.Purpose != "" || c.Target.EffectivePromptExecutionMode() != agentproto.PromptExecutionModeStartNew || c.Target.ThreadID != "" || c.Target.SourceThreadID != "" || c.CommandID == "" || req.Actor == "" || req.Actor != req.QueueActor {
		return
	}
	if r.ordinaryPending == nil {
		r.ordinaryPending = map[string]workMemoryOwner{}
	}
	r.ordinaryPending[c.CommandID] = workMemoryOwner{Surface: req.Surface, User: req.Actor, Instance: req.Instance}
}
func (r *workMemoryRuntime) noteOrdinaryThread(instance string, event agentproto.Event) bool {
	if (event.Kind != agentproto.EventTurnStarted && event.Kind != agentproto.EventThreadDiscovered) || event.Initiator.Kind != agentproto.InitiatorRemoteSurface || event.CommandID == "" || event.ThreadID == "" {
		return false
	}
	r.proofMu.Lock()
	defer r.proofMu.Unlock()
	owner, ok := r.ordinaryPending[event.CommandID]
	if !ok || owner.Instance != instance || owner.Surface != event.Initiator.SurfaceSessionID {
		return false
	}
	if r.ordinaryThreads == nil {
		r.ordinaryThreads = map[string]workMemoryOwner{}
	}
	r.ordinaryThreads[event.ThreadID] = owner
	delete(r.ordinaryPending, event.CommandID)
	return true
}
func (r *workMemoryRuntime) ordinaryAllows(req workMemoryRequest) bool {
	if req.Command.Target.ThreadID == "" || req.Command.Target.SourceThreadID != "" {
		return false
	}
	r.proofMu.RLock()
	defer r.proofMu.RUnlock()
	owner, ok := r.ordinaryThreads[req.Command.Target.ThreadID]
	return ok && owner.Surface == req.Surface && owner.User == req.Actor && owner.User == req.QueueActor
}
