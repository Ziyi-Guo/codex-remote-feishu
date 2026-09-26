package orchestrator

import (
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

const remoteTurnProgressHeartbeatInterval = 10 * time.Minute

// RecordRemoteTurnVisibleProgress counts only a message that reached the user.
func (s *Service) RecordRemoteTurnVisibleProgress(surfaceID, turnID string, deliveredAt time.Time) {
	s.turns.forEachActiveRemote(func(binding *remoteTurnBinding) {
		if binding.SurfaceSessionID == surfaceID && binding.TurnID == turnID && deliveredAt.After(binding.LastProgressAt) {
			binding.LastProgressAt = deliveredAt
		}
	})
}

func (s *Service) tickRemoteTurnProgressHeartbeat(now time.Time) []eventcontract.Event {
	var events []eventcontract.Event
	s.turns.forEachActiveRemote(func(binding *remoteTurnBinding) {
		if binding.TurnID == "" || binding.StartedAt.IsZero() || binding.InterruptRequested {
			return
		}
		inst := s.root.Instances[binding.InstanceID]
		if inst == nil || !inst.Online {
			return
		}
		lastVisible := binding.StartedAt
		if binding.LastProgressAt.After(lastVisible) {
			lastVisible = binding.LastProgressAt
		}
		if now.Before(lastVisible.Add(remoteTurnProgressHeartbeatInterval)) ||
			(!binding.LastPingAt.IsZero() && now.Before(binding.LastPingAt.Add(time.Minute))) {
			return
		}
		binding.LastPingAt = now
		events = append(events, eventcontract.Event{
			Kind:             eventcontract.KindNotice,
			SurfaceSessionID: binding.SurfaceSessionID,
			SourceMessageID:  binding.SourceMessageID,
			Meta: eventcontract.EventMeta{MessageDelivery: eventcontract.MessageDelivery{
				FirstSendLane: eventcontract.MessageLaneReplyThread,
			}},
			Notice: &control.Notice{
				Code:             "turn_progress_heartbeat",
				Title:            "任务状态 · 自动提醒",
				Text:             "任务仍在运行，尚未完成。过去 10 分钟没有新的进度说明。",
				DeliveryDedupKey: binding.TurnID,
			},
		})
	})
	return events
}
