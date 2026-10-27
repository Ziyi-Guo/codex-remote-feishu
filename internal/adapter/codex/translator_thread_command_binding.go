package codex

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

// A native request response proves the command/thread relationship even when
// turn/started arrives later, after another command has become pending.
func (t *Translator) createdThreadCommandBinding(message map[string]any, command agentproto.Command, threadID string) agentproto.Event {
	record := parseThreadRecord(lookupAny(message, "result", "thread"))
	event := buildThreadDiscoveredEvent(record, threadID, record.CWD, record.Name, "", false, record.RuntimeStatus)
	event.CommandID = command.CommandID
	event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: choose(command.Origin.Surface, command.Origin.ChatID)}
	if t.internalThreadIDs[threadID] {
		event.TrafficClass = agentproto.TrafficClassInternalHelper
		event.Initiator = agentproto.Initiator{Kind: agentproto.InitiatorInternalHelper}
	}
	return event
}
