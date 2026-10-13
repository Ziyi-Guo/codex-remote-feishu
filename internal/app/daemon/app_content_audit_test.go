package daemon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func TestGatewayContentAuditIsNotReportedAsRetryableTransportFailure(t *testing.T) {
	app := New(":0", ":0", nil, serverIdentityForTest())
	event := eventcontract.Event{SurfaceSessionID: "surface-1"}
	err := &feishu.APIError{API: "im.v1.message.reply", Code: 230028, Msg: "The messages do NOT pass the audit, ext=contain sensitive data: EMAIL_ADDRESS"}
	app.queueGatewayFailureNotice(event, err)
	app.queueGatewayFailureNotice(event, err)
	queued := app.pendingGlobalRuntimeNotices["surface-1"]
	if len(queued) != 1 {
		t.Fatalf("expected one audit rejection notice, got %d", len(queued))
	}
	notice := queued[0].Notice
	if notice.DeliveryDedupKey != "feishu_content_audit_rejected" || notice.Title != "消息未通过飞书内容审核" || !strings.Contains(fmt.Sprint(notice.Sections), "可重试：否") || strings.Contains(fmt.Sprint(notice.Sections), "可重试：是") {
		t.Fatalf("audit rejection was classified as retryable transport failure: %#v", notice)
	}
	delete(app.pendingGlobalRuntimeNotices, "surface-1")
	app.queueGatewayFailureNotice(queued[0], err)
	if len(app.pendingGlobalRuntimeNotices["surface-1"]) != 0 {
		t.Fatal("failed audit notice recursively queued another audit notice")
	}
}
