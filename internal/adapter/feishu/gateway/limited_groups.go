package gateway

import (
	"errors"
	"maps"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

// CloneLimitedGroups freezes policy maps and slices across gateway generations.
func CloneLimitedGroups(policies []limitedgroups.Policy) []limitedgroups.Policy {
	result := append([]limitedgroups.Policy(nil), policies...)
	for i := range result {
		result[i].AllowedUserIDs = append([]string(nil), result[i].AllowedUserIDs...)
		result[i].UserDailyLimits = maps.Clone(result[i].UserDailyLimits)
		result[i].ExecutionServers = append([]limitedgroups.ToolServer(nil), result[i].ExecutionServers...)
		for j := range result[i].ExecutionServers {
			result[i].ExecutionServers[j].Tools = append([]string(nil), result[i].ExecutionServers[j].Tools...)
		}
	}
	return result
}

// LimitedGroupPolicy preserves disabled/invalid chats as reserved. Ambiguous
// duplicate policies cannot accidentally turn into a broader grant.
func LimitedGroupPolicy(policies []limitedgroups.Policy, chatID string) (limitedgroups.Policy, bool) {
	var policy limitedgroups.Policy
	found := false
	for _, candidate := range policies {
		if candidate.ChatID != chatID {
			continue
		}
		if found {
			return limitedgroups.Policy{ChatID: chatID}, true
		}
		policy, found = candidate, true
	}
	return policy, found
}

type limitedInboundFailure struct {
	surfaceID, chatID, actorUserID, messageID string
	cause                                     error
}

func (e *limitedInboundFailure) Error() string { return e.cause.Error() }
func (e *limitedInboundFailure) Unwrap() error { return e.cause }

func limitedFailureBody(err error) string {
	if errors.Is(err, limitedgroups.ErrQuota) {
		return "你今天的使用次数已用完，请明天再试。"
	}
	return "当前无法核验使用额度，本条消息未处理，请稍后再试。"
}

func PlanInboundMessageEvent(env InboundEnv, event *larkim.P2MessageReceiveV1) (PlannedInboundMessage, bool, error) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return PlannedInboundMessage{}, false, nil
	}
	message := event.Event.Message
	chatID := xutil.StringValue(message.ChatId)
	policy, limited := LimitedGroupPolicy(env.LimitedGroups, chatID)
	if !limited {
		return planInboundMessageEvent(env, event, false)
	}
	sender := event.Event.Sender
	// Limited users must have the event's genuine open_id, never a fallback ID.
	if sender == nil || sender.SenderId == nil || xutil.StringValue(sender.SenderType) != "user" || xutil.StringValue(message.ChatType) != "group" || !policy.Allows(chatID, xutil.StringValue(sender.SenderId.OpenId)) {
		return PlannedInboundMessage{}, false, nil
	}
	userID := xutil.StringValue(sender.SenderId.OpenId)
	messageID := xutil.StringValue(message.MessageId)
	if messageID == "" || strings.TrimSpace(messageID) != messageID {
		return PlannedInboundMessage{}, false, nil
	}
	record := env.RecordSurfaceMessage
	env.RecordSurfaceMessage = func(string, string) {}
	plan, ok, err := planInboundMessageEvent(env, event, true)
	if err != nil || !ok {
		return plan, ok, err
	}
	surfaceID := ""
	if plan.Action != nil {
		surfaceID = plan.Action.SurfaceSessionID
	}
	if plan.Queue != nil {
		surfaceID = plan.Queue.surfaceID
	}
	if surfaceID == "" {
		return PlannedInboundMessage{}, false, nil
	}
	var duplicate bool
	if env.AcceptLimitedMessage == nil {
		err = limitedgroups.ErrLedger
	} else {
		duplicate, err = env.AcceptLimitedMessage(chatID, userID, messageID)
	}
	if err != nil {
		return PlannedInboundMessage{}, false, &limitedInboundFailure{surfaceID, chatID, userID, messageID, err}
	}
	if duplicate {
		return PlannedInboundMessage{}, false, nil
	}
	if record != nil && plan.Queue != nil {
		record(messageID, surfaceID)
	}
	return plan, true, nil
}

func limitedTopicRootID(topicRootID, userID string) string {
	// A known parent's topic already carries an actor prefix; re-scope it to the
	// real sender so replying to another user's topic never inherits their state.
	if strings.HasPrefix(topicRootID, "limited.") {
		parts := strings.SplitN(topicRootID, ".", 3)
		if len(parts) == 3 {
			topicRootID = parts[2]
		}
	}
	if topicRootID == "" || strings.Contains(topicRootID, "@") {
		return ""
	}
	return "limited." + userID + "." + topicRootID
}

var limitedActorID = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{1,128}$`)

// LimitedSurfaceID keeps Feishu's single @ topic separator and scopes routing
// by both the event actor and the underlying native topic.
func LimitedSurfaceID(gatewayID, chatID, actorUserID, topicRootID string) string {
	if !limitedActorID.MatchString(actorUserID) {
		return ""
	}
	topic := limitedTopicRootID(topicRootID, actorUserID)
	if topic == "" {
		return ""
	}
	return SurfaceIDForInboundTopic(gatewayID, chatID, "group", actorUserID, topic)
}

// LimitedSurfaceIdentity reads the actor encoded in the target surface; callers
// must compare it with verified event identity rather than infer it from a click.
func LimitedSurfaceIdentity(surfaceID string) (actorUserID, topicRootID string, ok bool) {
	ref, valid := feishuidentity.ParseSurfaceRef(surfaceID)
	if !valid || !ref.IsChat() {
		return "", "", false
	}
	parts := strings.SplitN(ref.TopicRootID(), ".", 3)
	if len(parts) != 3 || parts[0] != "limited" || !limitedActorID.MatchString(parts[1]) || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}
