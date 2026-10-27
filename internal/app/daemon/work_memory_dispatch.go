package daemon

import (
	"path/filepath"
	"reflect"
	"strings"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func (a *App) workMemoryRequestLocked(surfaceID, instanceID string, c agentproto.Command) workMemoryRequest {
	req := workMemoryRequest{Surface: surfaceID, Actor: c.Origin.UserID, Chat: c.Origin.ChatID, Message: c.Origin.MessageID, Instance: instanceID, Command: c}
	s := a.service.Surface(surfaceID)
	if s == nil {
		return req
	}
	item := s.QueueItems[s.ActiveQueueItemID]
	if item != nil {
		req.QueueActor = item.ActorUserID
		req.UserInput = item.SourceKind == state.QueueItemSourceUser && item.AutoContinueEpisodeID == ""
	}
	ref, parsed := feishuidentity.ParseSurfaceRef(surfaceID)
	_, _, encoded := gatewaypkg.LimitedSurfaceIdentity(surfaceID)
	_, limited, err := a.limitedPolicy(ref.GatewayID, ref.ChatID())
	req.Authorized = c.Overrides.LimitedExecution == nil && s.LimitedExecution == nil && !encoded && !limited && err == nil && parsed && c.Origin.Surface == surfaceID && c.Origin.ChatID == s.ChatID && c.Origin.UserID != "" && item != nil && item.ActorUserID == c.Origin.UserID && item.SourceMessageID == c.Origin.MessageID && c.Origin.MessageID != ""
	req.Eligible = req.Authorized && c.Kind == agentproto.CommandPromptSend && !c.Target.InternalHelper && c.Target.Purpose == "" && req.UserInput
	for _, instance := range a.service.Instances() {
		for id := range instance.Threads {
			req.KnownThreads = append(req.KnownThreads, id)
		}
	}
	return req
}

// Caller owns a.mu. Snapshot I/O happens without it, then identity is rechecked.
func (a *App) prepareWorkMemoryCommandLocked(surfaceID, instanceID string, c agentproto.Command) (agentproto.Command, error) {

	switch c.Kind {
	case agentproto.CommandPromptSend:
	case agentproto.CommandTurnSteer, agentproto.CommandReviewStart, agentproto.CommandThreadCompactStart, agentproto.CommandThreadHistoryRead, agentproto.CommandThreadRead, agentproto.CommandThreadShellCommand, agentproto.CommandThreadGoalGet:
		for _, thread := range []string{c.Target.ThreadID, c.Target.SourceThreadID} {
			if thread != "" {
				if err := a.guardWorkMemoryHistoryLocked(surfaceID, instanceID, thread); err != nil {
					return c, err
				}
			}
		}
		return c, nil
	default:
		return c, nil
	}
	base := a.headlessRuntime.Paths.StateDir
	if base == "" {
		return c, nil
	}
	if a.workMemory == nil {
		a.workMemory = &workMemoryRuntime{root: filepath.Join(base, "work-memory"), lifecycle: a.daemonLifecycleID, busy: make(chan struct{}, 1)}
	}
	req := a.workMemoryRequestLocked(surfaceID, instanceID, c)
	runtime := a.workMemory
	a.mu.Unlock()
	result := runtime.prepare(req)
	a.mu.Lock()
	if result.Err != nil {
		return c, result.Err
	}
	current := a.workMemoryRequestLocked(surfaceID, instanceID, c)
	current.KnownThreads = nil
	req.KnownThreads = nil
	if !reflect.DeepEqual(req, current) || a.service.AttachedInstanceID(surfaceID) != instanceID {
		return c, workMemoryGuardBusy(req)
	}
	if result.Text != "" {
		inputs := make([]agentproto.Input, 0, len(c.Prompt.Inputs)+1)
		inputs = append(inputs, agentproto.Input{Type: agentproto.InputText, Text: result.Text})
		c.Prompt.Inputs = append(inputs, c.Prompt.Inputs...)
	}
	if result.DeveloperInstruction != "" {
		c.CodexResume = agentproto.CloneCodexResumePolicy(c.CodexResume)
		c.CodexResume.DeveloperInstruction = strings.TrimSpace(c.CodexResume.DeveloperInstruction + "\n\n" + result.DeveloperInstruction)
	}
	return c, nil
}

func (a *App) guardWorkMemoryHistoryLocked(surfaceID, instanceID, threadID string) error {
	if a.headlessRuntime.Paths.StateDir == "" {
		return nil
	}
	if a.workMemory == nil {
		a.workMemory = &workMemoryRuntime{root: filepath.Join(a.headlessRuntime.Paths.StateDir, "work-memory"), lifecycle: a.daemonLifecycleID, busy: make(chan struct{}, 1)}
	}
	req := a.workMemoryViewerRequestLocked(surfaceID, instanceID, threadID)
	runtime := a.workMemory
	a.mu.Unlock()
	result := runtime.prepare(req)
	a.mu.Lock()
	current := a.workMemoryViewerRequestLocked(surfaceID, instanceID, threadID)
	if current.Actor != req.Actor || current.Authorized != req.Authorized {
		return workMemoryGuardBusy(req)
	}
	return result.Err
}

func (a *App) workMemoryViewerRequestLocked(surfaceID, instanceID, threadID string) workMemoryRequest {
	actor := a.service.SurfaceActorUserID(surfaceID)
	req := a.workMemoryRequestLocked(surfaceID, instanceID, agentproto.Command{Origin: agentproto.Origin{Surface: surfaceID, UserID: actor, ChatID: a.service.SurfaceChatID(surfaceID)}, Target: agentproto.Target{ThreadID: threadID}})
	req.QueueActor = actor
	ref, parsed := feishuidentity.ParseSurfaceRef(surfaceID)
	_, _, encoded := gatewaypkg.LimitedSurfaceIdentity(surfaceID)
	_, limited, err := a.limitedPolicy(ref.GatewayID, ref.ChatID())
	req.Authorized = parsed && actor != "" && !encoded && !limited && err == nil && a.service.LimitedExecutionForSurface(surfaceID) == nil
	req.Eligible = false
	return req
}
