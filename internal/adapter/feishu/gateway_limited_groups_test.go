package feishu

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func TestLiveGatewayLimitedGroupLedgerSurvivesRestart(t *testing.T) {
	policy := limitedgroups.Policy{Enabled: true, ChatID: "oc_limited", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner", "ou_guest"}, Workspace: "/workspace/limited", DailyLimit: 1}
	cfg := LiveGatewayConfig{GatewayID: "limited", LimitedGroups: []limitedgroups.Policy{policy}, LimitedGroupsUsagePath: filepath.Join(t.TempDir(), "usage.json")}
	gateway := NewLiveGateway(cfg)
	// External policy mutation must not change a running gateway's authorization.
	cfg.LimitedGroups[0].AllowedUserIDs[1] = "ou_stranger"
	env := gateway.inboundEnv()
	if !env.LimitedGroups[0].Allows("oc_limited", "ou_guest") {
		t.Fatal("live policy shared caller slice")
	}
	duplicate, err := env.AcceptLimitedMessage("oc_limited", "ou_guest", "om_first")
	if err != nil || duplicate {
		t.Fatalf("first admission: %v %v", duplicate, err)
	}
	cfg.LimitedGroups = env.LimitedGroups
	restarted := NewLiveGateway(cfg)
	duplicate, err = restarted.inboundEnv().AcceptLimitedMessage("oc_limited", "ou_guest", "om_first")
	if err != nil || !duplicate {
		t.Fatalf("restart dedupe: %v %v", duplicate, err)
	}
	_, err = restarted.inboundEnv().AcceptLimitedMessage("oc_limited", "ou_guest", "om_second")
	if !errors.Is(err, limitedgroups.ErrQuota) {
		t.Fatalf("restart quota bypass: %v", err)
	}
}

func TestLiveGatewayLimitedLedgerFailureFailsClosed(t *testing.T) {
	policy := limitedgroups.Policy{Enabled: true, ChatID: "oc_limited", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner"}, Workspace: "/workspace/limited", DailyLimit: 25}
	gateway := NewLiveGateway(LiveGatewayConfig{GatewayID: "limited", LimitedGroups: []limitedgroups.Policy{policy}})
	_, err := gateway.inboundEnv().AcceptLimitedMessage("oc_limited", "ou_owner", "om_first")
	if !errors.Is(err, limitedgroups.ErrLedger) {
		t.Fatalf("missing ledger did not fail closed: %v", err)
	}
}

func TestLiveGatewayLimitedQuotaReceiptIsText(t *testing.T) {
	gateway := NewLiveGateway(LiveGatewayConfig{GatewayID: "limited"})
	replies := 0
	gateway.replyMessageFn = func(_ context.Context, messageID, msgType, content string, inThread bool) (*larkim.ReplyMessageResp, error) {
		replies++
		if messageID != "om_request" || msgType != "text" {
			t.Fatalf("quota receipt not native text: %s %s %s", messageID, msgType, content)
		}
		id := "om_reply"
		return &larkim.ReplyMessageResp{Data: &larkim.ReplyMessageRespData{MessageId: &id}}, nil
	}
	gateway.deliverLimitedInboundFailure(context.Background(), "feishu:limited:chat:oc_limited@limited.ou_owner.om_root", "oc_limited", "ou_owner", "om_request", "quota exhausted")
	if replies != 1 {
		t.Fatalf("missing receipt: %d", replies)
	}
}

func TestGatewayConfigFreezesLimitedGroups(t *testing.T) {
	cfg := normalizeGatewayAppConfig(GatewayAppConfig{GatewayID: "limited", LimitedGroups: []limitedgroups.Policy{{AllowedUserIDs: []string{"ou_owner"}, UserDailyLimits: map[string]int{"ou_owner": 5}}}})
	cloned := gatewaypkg.CloneLimitedGroups(cfg.LimitedGroups)
	cfg.LimitedGroups[0].AllowedUserIDs[0] = "changed"
	cfg.LimitedGroups[0].UserDailyLimits["ou_owner"] = 9
	if cloned[0].AllowedUserIDs[0] != "ou_owner" || cloned[0].UserDailyLimits["ou_owner"] != 5 {
		t.Fatal("policy snapshot shared mutable data")
	}
}
