package gateway

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func limitedTestPolicy() limitedgroups.Policy {
	return limitedgroups.Policy{Enabled: true, ChatID: "oc_limited", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner", "ou_guest"}, Workspace: "/workspace/limited", DailyLimit: 25}
}
func limitedTestEvent(kind, content string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: stringRef("ou_guest")}, SenderType: stringRef("user")},
		Message: &larkim.EventMessage{MessageId: stringRef("om_message"), ChatId: stringRef("oc_limited"), ChatType: stringRef("group"), RootId: stringRef("om_root"), MessageType: stringRef(kind), Content: stringRef(content), Mentions: []*larkim.MentionEvent{{Key: stringRef("@_user_1"), Id: &larkim.UserId{OpenId: stringRef("ou_bot")}}}},
	}}
}
func limitedTestEnv() InboundEnv {
	return InboundEnv{GatewayID: "limited", BotOpenID: "ou_bot", LimitedGroups: []limitedgroups.Policy{limitedTestPolicy()}, ParseTextActionWithoutCatalog: parseTextAction, RecordSurfaceMessage: func(string, string) {}, AcceptLimitedMessage: func(string, string, string) (bool, error) { return false, nil }}
}

func TestLimitedGroupRejectsBeforeLogsOrContentAccess(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*InboundEnv, *larkim.P2MessageReceiveV1)
	}{
		{"not_allowed", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) {
			e.Event.Sender.SenderId.OpenId = stringRef("ou_stranger")
		}},
		{"missing_sender", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) { e.Event.Sender = nil }},
		{"missing_type", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) { e.Event.Sender.SenderType = nil }},
		{"bot_sender", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) { e.Event.Sender.SenderType = stringRef("app") }},
		{"fallback_identity", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) {
			e.Event.Sender.SenderId = &larkim.UserId{UserId: stringRef("ou_guest")}
		}},
		{"disabled", func(env *InboundEnv, _ *larkim.P2MessageReceiveV1) { env.LimitedGroups[0].Enabled = false }},
		{"invalid", func(env *InboundEnv, _ *larkim.P2MessageReceiveV1) { env.LimitedGroups[0].DailyLimit = 0 }},
		{"ambiguous", func(env *InboundEnv, _ *larkim.P2MessageReceiveV1) {
			env.LimitedGroups = append(env.LimitedGroups, limitedTestPolicy())
		}},
		{"wrong_chat_type", func(_ *InboundEnv, e *larkim.P2MessageReceiveV1) { e.Event.Message.ChatType = stringRef("p2p") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			env := limitedTestEnv()
			event := limitedTestEvent("text", "SECRET invalid JSON")
			fail := func() { t.Fatal("unauthorized message accessed state or quota") }
			env.LookupSurfaceMessage = func(string) string { fail(); return "" }
			env.RecordSurfaceMessage = func(string, string) { fail() }
			env.AcceptLimitedMessage = func(string, string, string) (bool, error) { fail(); return false, nil }
			tc.mutate(&env, event)
			_, ok, err := PlanInboundMessageEvent(env, event)
			if ok || err != nil || output.Len() != 0 {
				t.Fatalf("rejection leaked state/logs: ok=%v err=%v logs=%q", ok, err, output.String())
			}
		})
	}
}

func TestLimitedGroupKeepsNativeTextMediaAndActorTopics(t *testing.T) {
	cases := []struct {
		kind, content string
		actionKind    control.ActionKind
	}{
		{"text", `{"text":"@_user_1 hello"}`, control.ActionTextMessage},
		{"post", `{"content":[[{"tag":"text","text":"hello"}]]}`, control.ActionTextMessage},
		{"image", `{"image_key":"img_key"}`, control.ActionImageMessage},
		{"file", `{"file_key":"file_key","file_name":"sample.txt"}`, control.ActionFileMessage},
		{"merge_forward", `{}`, control.ActionTextMessage},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			env := limitedTestEnv()
			accepts, dispatches := 0, 0
			env.AcceptLimitedMessage = func(chat, user, message string) (bool, error) {
				accepts++
				if chat != "oc_limited" || user != "ou_guest" || message != "om_message" {
					t.Fatal("wrong identity")
				}
				return false, nil
			}
			env.ParsePostInputs = func(context.Context, string, string) ([]agentproto.Input, string, error) {
				return []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}}, "hello", nil
			}
			env.BuildMergeForwardStructuredInput = func(context.Context, *larkim.EventMessage) (string, []agentproto.Input, error) {
				return "forward", []agentproto.Input{{Type: agentproto.InputText, Text: "forward"}}, nil
			}
			env.DownloadImage = func(context.Context, string, string) (string, string, error) {
				return "/tmp/image.png", "image/png", nil
			}
			env.DownloadFile = func(context.Context, string, string, string) (string, error) { return "/tmp/sample.txt", nil }
			err := HandleInboundMessageEvent(context.Background(), env, limitedTestEvent(tc.kind, tc.content), nil, func(_ context.Context, a control.Action) error {
				dispatches++
				if a.Kind != tc.actionKind || a.ActorUserID != "ou_guest" || a.ChatID != "oc_limited" {
					t.Fatalf("wrong action: %#v", a)
				}
				ref, ok := feishuidentity.ParseSurfaceRef(a.SurfaceSessionID)
				if !ok || ref.TopicRootID() != "limited.ou_guest.om_root" || strings.Count(a.SurfaceSessionID, "@") != 1 {
					t.Fatalf("invalid isolated surface: %s", a.SurfaceSessionID)
				}
				return nil
			})
			if err != nil || accepts != 1 || dispatches != 1 {
				t.Fatalf("err=%v accepts=%d dispatches=%d", err, accepts, dispatches)
			}
		})
	}
	if a, b := limitedTopicRootID("om_root", "ou_owner"), limitedTopicRootID("limited.ou_owner.om_root", "ou_guest"); a == b || b != "limited.ou_guest.om_root" {
		t.Fatalf("actor collision %q %q", a, b)
	}
}

func TestLimitedGroupQuotaRejectsBeforeReadsAndReturnsReceiptOnlyFromHandler(t *testing.T) {
	for _, quotaErr := range []error{limitedgroups.ErrQuota, limitedgroups.ErrLedger} {
		t.Run(quotaErr.Error(), func(t *testing.T) {
			env := limitedTestEnv()
			receipts := 0
			env.AcceptLimitedMessage = func(string, string, string) (bool, error) { return false, quotaErr }
			env.RecordSurfaceMessage = func(string, string) { t.Fatal("quota failure recorded message") }
			env.DownloadImage = func(context.Context, string, string) (string, string, error) {
				t.Fatal("quota failure downloaded image")
				return "", "", nil
			}
			env.QuotedMessageInputs = func(context.Context, *larkim.EventMessage) QuotedMessageInputs {
				t.Fatal("quota failure fetched quote")
				return QuotedMessageInputs{}
			}
			env.DeliverLimitedInboundFailure = func(_ context.Context, surface, chat, user, message, body string) {
				receipts++
				if chat != "oc_limited" || user != "ou_guest" || body == "" {
					t.Fatal("wrong receipt")
				}
			}
			event := limitedTestEvent("image", `{"image_key":"img_key"}`)
			_, ok, err := PlanInboundMessageEvent(env, event)
			if ok || !errors.Is(err, quotaErr) || receipts != 0 {
				t.Fatalf("planner side effect or missing rejection: %v %v", ok, err)
			}
			err = HandleInboundMessageEvent(context.Background(), env, event, nil, func(context.Context, control.Action) error { t.Fatal("quota dispatched"); return nil })
			if err != nil || receipts != 1 {
				t.Fatalf("missing receipt: %v %d", err, receipts)
			}
		})
	}
}

func TestLimitedGroupIgnoresDuplicateMentionOnlyAndMissingMentionWithoutQuota(t *testing.T) {
	for _, scenario := range []string{"duplicate", "mention_only", "missing_mention", "invalid_content"} {
		t.Run(scenario, func(t *testing.T) {
			env := limitedTestEnv()
			event := limitedTestEvent("text", `{"text":"@_user_1 hello"}`)
			accepts := 0
			env.AcceptLimitedMessage = func(string, string, string) (bool, error) { accepts++; return true, nil }
			switch scenario {
			case "mention_only":
				event.Event.Message.Content = stringRef(`{"text":"@_user_1 "}`)
			case "missing_mention":
				event.Event.Message.Mentions = nil
			case "invalid_content":
				event.Event.Message.Content = stringRef(`invalid`)
			}
			_, ok, _ := PlanInboundMessageEvent(env, event)
			expected := 0
			if scenario == "duplicate" {
				expected = 1
			}
			if ok || accepts != expected {
				t.Fatalf("ok=%v accepts=%d expected=%d", ok, accepts, expected)
			}
		})
	}
}

func TestLimitedGroupOtherChatsKeepExistingBehavior(t *testing.T) {
	env := limitedTestEnv()
	env.AcceptLimitedMessage = func(string, string, string) (bool, error) { t.Fatal("ordinary chat quota"); return false, nil }
	event := limitedTestEvent("text", `{"text":"@_user_1 hello"}`)
	event.Event.Message.ChatId = stringRef("oc_ordinary")
	event.Event.Sender.SenderType = nil
	plan, ok, err := PlanInboundMessageEvent(env, event)
	if err != nil || !ok || plan.Queue == nil || strings.Contains(plan.Queue.surfaceID, "limited.ou_") {
		t.Fatalf("ordinary behavior changed: %v %v %#v", ok, err, plan)
	}
}

func TestLimitedSurfaceIdentityUsesEncodedActor(t *testing.T) {
	surface := LimitedSurfaceID("restricted", "oc_limited", "ou_guest", "om_root")
	actor, topic, ok := LimitedSurfaceIdentity(surface)
	if !ok || actor != "ou_guest" || topic != "om_root" {
		t.Fatalf("identity round trip: %s %s %v", actor, topic, ok)
	}
	for _, invalid := range []string{"feishu:restricted:chat:oc_limited@om_root", "feishu:restricted:chat:oc_limited@limited.ou_guest.", "feishu:restricted:chat:oc_limited@limited.ou_guest.om_root@extra", "feishu:restricted:user:ou_guest"} {
		if _, _, ok := LimitedSurfaceIdentity(invalid); ok {
			t.Fatalf("invalid surface accepted: %s", invalid)
		}
	}
	if LimitedSurfaceID("restricted", "oc_limited", "ou_guest", "om_root@extra") != "" {
		t.Fatal("accepted second topic separator")
	}
}

func TestCloneLimitedGroupExecutionTools(t *testing.T) {
	original := []limitedgroups.Policy{{ExecutionServers: []limitedgroups.ToolServer{{Name: "source", Tools: []string{"read"}}}}}
	clone := CloneLimitedGroups(original)
	original[0].ExecutionServers[0].Name = "changed"
	original[0].ExecutionServers[0].Tools[0] = "write"
	if clone[0].ExecutionServers[0].Name != "source" || clone[0].ExecutionServers[0].Tools[0] != "read" {
		t.Fatal("execution allowlist shares caller memory")
	}
}
