package feishu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageEmailAuditRedactsBeforeSingleRetry(t *testing.T) {
	for _, api := range []string{"create", "reply", "patch"} {
		for _, payload := range []string{
			`{"text":"继续使用 person@example.com，密码已修改。"}`,
			`{"zh_cn":{"title":"注册完成","content":[[{"tag":"text","text":"person@example.com"}]]}}`,
			`{"elements":[{"tag":"markdown","content":"邮箱 person@example.com 已登录"},{"tag":"button","text":{"tag":"plain_text","content":"继续"},"value":{"kind":"confirm","surface":"surface-1"}}]}`,
		} {
			msgType := "text"
			if strings.Contains(payload, "zh_cn") {
				msgType = "post"
			} else if strings.Contains(payload, "elements") {
				msgType = "interactive"
			}
			if api == "patch" && msgType != "interactive" {
				continue
			}
			t.Run(api+payload[:8], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "tenant_access_token") {
						_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
						return
					}
					calls++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					content, _ := body["content"].(string)
					if api == "reply" && body["reply_in_thread"] != true {
						t.Error("redaction lost topic reply semantics")
					}
					if calls == 1 {
						if content != payload {
							t.Error("first attempt altered an unaudited message")
						}
						_, _ = w.Write([]byte(`{"code":230028,"msg":"The messages do NOT pass the audit, ext=contain sensitive data: EMAIL_ADDRESS"}`))
						return
					}
					if calls > 2 || strings.Contains(content, "person@example.com") || !strings.Contains(content, "邮箱地址已移除") || !json.Valid([]byte(content)) {
						t.Errorf("unexpected sanitized attempt: calls=%d content=%s", calls, content)
					}
					if strings.Contains(payload, "surface-1") && !strings.Contains(content, "surface-1") {
						t.Error("redaction changed callback ownership")
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
				}))
				defer server.Close()
				g := NewLiveGateway(LiveGatewayConfig{GatewayID: "audit-test", AppID: "audit-test", AppSecret: "test", Domain: server.URL})
				var err error
				switch api {
				case "create":
					_, err = g.createMessage(t.Context(), "chat_id", "oc_chat", msgType, payload)
				case "reply":
					_, err = g.replyMessage(t.Context(), "om_source", msgType, payload, true)
				case "patch":
					_, err = g.patchMessage(t.Context(), "om_source", payload)
				}
				if err != nil || calls != 2 {
					t.Fatalf("redacted delivery failed: calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func TestMessageAuditDoesNotRepeatRejectedOrUnsafeContent(t *testing.T) {
	for _, tc := range []struct {
		name, payload, reason string
		code, attempts        int
	}{
		{"still rejected", `{"text":"person@example.com"}`, "EMAIL_ADDRESS", 230028, 2},
		{"other sensitive data", `{"text":"person@example.com"}`, "PHONE_NUMBER", 230028, 1},
		{"other failure", `{"text":"person@example.com"}`, "EMAIL_ADDRESS", 230001, 1},
		{"nothing removable", `{"text":"already removed"}`, "EMAIL_ADDRESS", 230028, 1},
		{"invalid payload", `not json person@example.com`, "EMAIL_ADDRESS", 230028, 1},
		{"callback data", `{"elements":[{"content":"person@example.com","value":{"email":"person@example.com"}}]}`, "EMAIL_ADDRESS", 230028, 1},
		{"link target", `{"href":"mailto:person@example.com","text":"person@example.com"}`, "EMAIL_ADDRESS", 230028, 1},
		{"markdown link target", `{"text":"邮箱 [联系](mailto:person@example.com)"}`, "EMAIL_ADDRESS", 230028, 1},
		{"URL in display text", `{"text":"https://example.org/reset?email=person@example.com"}`, "EMAIL_ADDRESS", 230028, 1},
		{"success unchanged", `{"text":"person@example.com"}`, "", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
					return
				}
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"code": tc.code, "msg": "audit " + tc.reason, "data": map[string]string{"message_id": "om_sent"}})
			}))
			defer server.Close()
			g := NewLiveGateway(LiveGatewayConfig{GatewayID: "audit-test", AppID: "audit-test", AppSecret: "test", Domain: server.URL})
			_, err := g.replyMessage(t.Context(), "om_source", "interactive", tc.payload, true)
			if calls != tc.attempts || (err == nil) != (tc.code == 0) {
				t.Fatalf("calls=%d want=%d err=%v", calls, tc.attempts, err)
			}
		})
	}
}
