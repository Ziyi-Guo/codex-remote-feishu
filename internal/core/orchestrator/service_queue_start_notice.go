package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

const queuedMessageStartedText = "开始执行这条排队消息。"

type dispatchNextOptions struct {
	suppressQueuedStartNoticeQueueItemID string
}

func (opts dispatchNextOptions) suppressesQueuedStartNotice(item *state.QueueItemRecord) bool {
	return item != nil && strings.TrimSpace(opts.suppressQueuedStartNoticeQueueItemID) == strings.TrimSpace(item.ID)
}

func (s *Service) queuedMessageStartedEvent(surface *state.SurfaceConsoleRecord, item *state.QueueItemRecord) *eventcontract.Event {
	if surface == nil || item == nil || item.SourceKind != state.QueueItemSourceUser {
		return nil
	}
	replyToMessageID := strings.TrimSpace(xutil.FirstNonEmpty(item.ReplyToMessageID, item.SourceMessageID))
	if replyToMessageID == "" {
		return nil
	}
	replyToMessagePreview := strings.TrimSpace(xutil.FirstNonEmpty(item.ReplyToMessagePreview, item.SourceMessagePreview))
	text := queuedMessageStartedText
	return &eventcontract.Event{
		Kind:                 eventcontract.KindTimelineText,
		GatewayID:            surface.GatewayID,
		SurfaceSessionID:     surface.SurfaceSessionID,
		SourceMessageID:      replyToMessageID,
		SourceMessagePreview: replyToMessagePreview,
		TimelineText: &control.TimelineText{
			ThreadID:              queuedItemExecutionThreadID(item),
			Type:                  control.TimelineTextQueuedMessageStarted,
			Text:                  text,
			ReplyToMessageID:      replyToMessageID,
			ReplyToMessagePreview: replyToMessagePreview,
		},
	}
}

// Report only the model evidence captured for this started turn, never a request default.
func (s *Service) turnModelStartedEvent(surface *state.SurfaceConsoleRecord, item *state.QueueItemRecord, binding *remoteTurnBinding) *eventcontract.Event {
	if queuedItemPromptDispatchPlan(item).Purpose == agentproto.PromptPurposeReview {
		return nil
	}
	event := s.queuedMessageStartedEvent(surface, item)
	if event == nil {
		return nil
	}
	model := binding.Model
	if model == "" {
		model = "暂未获得运行确认"
	} else if binding.ReasoningEffort != "" {
		model += " / " + binding.ReasoningEffort
	}
	event.TimelineText.Type = control.TimelineTextTurnModelStarted
	event.TimelineText.TurnID = binding.TurnID
	event.TimelineText.Text = "本次回复模型：" + model + "。"
	return event
}
