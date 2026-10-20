package orchestrator

import (
	"reflect"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func (s *Service) LimitedExecutionForSurface(surfaceID string) *limitedgroups.ExecutionPolicy {
	if surface := s.root.Surfaces[surfaceID]; surface != nil {
		return surface.LimitedExecution
	}
	return nil
}
func (s *Service) InstanceSupportsLimitedGroups(instanceID, workdir string) bool {
	inst := s.root.Instances[instanceID]
	return inst != nil && inst.Managed && inst.Capabilities.LimitedGroups && inst.WorkspaceRoot == workdir && state.EffectiveInstanceBackend(inst) == agentproto.BackendCodex
}

// PrepareLimitedGroup starts a private workspace; it cannot attach personal instances.
func (s *Service) PrepareLimitedGroup(action control.Action) ([]eventcontract.Event, bool) {
	p := action.LimitedExecution
	if p == nil {
		return nil, false
	}
	if p.Validate() != nil {
		return nil, true
	}
	surface := s.ensureSurface(action)
	s.setSurfaceDesiredContract(surface, state.SurfaceBackendContract{ProductMode: state.ProductModeNormal, Backend: agentproto.BackendCodex})
	if surface.AttachedInstanceID != "" {
		return nil, false
	}
	if surface.PendingHeadless != nil {
		return nil, true
	}
	if action.Kind != control.ActionTextMessage {
		return notice(surface, "limited_start_with_text", "请先发送文字开始查询。"), true
	}
	events := s.startWorkspaceHeadlessLaunchWithOverlayCleanup(surface, p.Workdir, workspaceHeadlessLaunchOptions{PrepareNewThread: true, NoticeCode: "limited_group_starting", NoticeTitle: "正在准备查询", NoticeText: "正在准备独立会话，原问题会自动继续。"})
	return events, true
}

func (s *Service) limitedActionBlocked(surface *state.SurfaceConsoleRecord, action control.Action) []eventcontract.Event {
	p := surface.LimitedExecution
	if p == nil {
		return nil
	}
	if p.Validate() != nil || action.LimitedExecution == nil || !reflect.DeepEqual(p, action.LimitedExecution) {
		return notice(surface, "limited_policy_missing", "当前查询入口未通过校验。")
	}
	if !LimitedActionAllowed(action.Kind) {
		return notice(surface, "limited_action_blocked", "此群用于查询；连接、权限和工作区设置由所有者管理。")
	}
	if action.Request != nil || action.RequestControl != nil || surface.ActiveRequestCapture != nil {
		requestType := ""
		if action.Request != nil {
			requestType = action.Request.RequestType
		}
		if requestType != "request_user_input" && action.ActorUserID != p.OwnerUserID {
			return notice(surface, "limited_owner_required", "这项操作需要所有者本人确认。")
		}
	}
	return nil
}

func LimitedActionAllowed(kind control.ActionKind) bool {
	switch kind {
	case control.ActionTextMessage, control.ActionImageMessage, control.ActionFileMessage, control.ActionStatus, control.ActionStop, control.ActionNewThread, control.ActionShowCommandHelp:
		return true
	}
	return strings.HasPrefix(string(kind), "surface.request.")
}

func limitedRequestResponseAllowed(surface *state.SurfaceConsoleRecord, requestType string, action control.Action) bool {
	if surface.LimitedExecution == nil || requestType == "request_user_input" {
		return true
	}
	if action.ActorUserID != surface.LimitedExecution.OwnerUserID || action.Request == nil {
		return false
	}
	option := control.NormalizeRequestOptionID(action.Request.RequestOptionID)
	if requestType == "permissions_request_approval" {
		return option == "decline"
	}
	return option == "accept" || option == "decline" || option == "cancel"
}

func limitRequestOptions(surface *state.SurfaceConsoleRecord, record *state.RequestPromptRecord) {
	if surface.LimitedExecution == nil || record.RequestType == "request_user_input" {
		return
	}
	var options []state.RequestPromptOptionRecord
	for _, option := range record.Options {
		id := control.NormalizeRequestOptionID(option.OptionID)
		if id == "decline" || id == "cancel" || (id == "accept" && record.RequestType != "permissions_request_approval") {
			options = append(options, option)
		}
	}
	record.Options = options
	record.HintText = "仅所有者可确认本次操作；不提供会话或永久授权。"
}
