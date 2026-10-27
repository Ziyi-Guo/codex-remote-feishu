package daemon

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

func shouldLogAgentEvent(event agentproto.Event) bool {
	switch event.Kind {
	case agentproto.EventItemDelta:
		return false
	default:
		return true
	}
}

func eventAffectsSurfaceResumeState(event agentproto.Event) bool {
	switch event.Kind {
	case agentproto.EventThreadsSnapshot,
		agentproto.EventThreadDiscovered,
		agentproto.EventThreadRuntimeStatusUpdated,
		agentproto.EventThreadFocused,
		agentproto.EventItemCompleted,
		agentproto.EventTurnCompleted:
		return true
	default:
		return false
	}
}

func eventAffectsBotCapabilitySettingsState(event agentproto.Event) bool {
	return event.Kind == agentproto.EventRequestResolved
}
