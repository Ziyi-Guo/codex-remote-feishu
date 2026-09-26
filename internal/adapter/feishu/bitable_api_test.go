package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func newTestBitableAPI(t *testing.T, handler http.HandlerFunc) *liveBitableAPI {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := lark.NewClient(t.Name(), "secret", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false))
	return &liveBitableAPI{client: client, broker: NewFeishuCallBroker("test", client)}
}

func TestBitableListRecordsSearchPagination(t *testing.T) {
	for _, fields := range [][]string{nil, {}, {"工作区名称", "字段\"换行\n"}} {
		t.Run(fmt.Sprintf("fields=%q", fields), func(t *testing.T) {
			calls := 0
			api := newTestBitableAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/open-apis/bitable/v1/apps/app/tables/table/records/search" {
					t.Errorf("request = %s %s, want POST records/search", r.Method, r.URL.Path)
				}
				query := r.URL.Query()
				if query.Get("page_size") != "500" || query.Has("field_names") {
					t.Errorf("query = %v, want page_size=500 without field_names", query)
				}
				wantToken := ""
				if calls == 2 {
					wantToken = "next+/="
				}
				if query.Get("page_token") != wantToken || calls == 1 && query.Has("page_token") {
					t.Errorf("page_token = %q, want %q", query.Get("page_token"), wantToken)
				}
				body := map[string]json.RawMessage{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode search body: %v", err)
				}
				if len(fields) == 0 {
					if len(body) != 0 {
						t.Errorf("all-fields body = %s, want {}", body)
					}
				} else {
					var got []string
					if err := json.Unmarshal(body["field_names"], &got); err != nil || !reflect.DeepEqual(got, fields) || len(body) != 1 {
						t.Errorf("body = %s, want only field_names=%q (err=%v)", body, fields, err)
					}
				}
				if calls == 1 {
					fmt.Fprint(w, `{"code":0,"data":{"items":[{"record_id":"rec1","fields":{"名称":"一","启用":true}}],"has_more":true,"page_token":"next+/="}}`)
				} else {
					fmt.Fprint(w, `{"code":0,"data":{"items":[{"record_id":"rec2","fields":{"名称":"二","次数":2}}],"has_more":false}}`)
				}
			})
			records, err := api.ListRecords(context.Background(), "app", "table", fields)
			if err != nil || calls != 2 || len(records) != 2 {
				t.Fatalf("ListRecords: len=%d calls=%d err=%v", len(records), calls, err)
			}
			if *records[0].RecordId != "rec1" || *records[1].RecordId != "rec2" || records[0].Fields["名称"] != "一" || records[0].Fields["启用"] != true || records[1].Fields["次数"] != float64(2) {
				t.Fatalf("records lost IDs or fields: %#v / %#v", records[0], records[1])
			}
		})
	}
}

func TestBitableListRecordsSearchErrorAndContext(t *testing.T) {
	calls := 0
	api := newTestBitableAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Request-Id", "request-search")
		fmt.Fprint(w, `{"code":1254004,"msg":"WrongTableId"}`)
	})
	records, err := api.ListRecords(context.Background(), "app", "table", nil)
	var apiErr *APIError
	if records != nil || !errors.As(err, &apiErr) || apiErr.API != "bitable.v1.app_table_record.search" || apiErr.Code != 1254004 || apiErr.RequestID != "request-search" {
		t.Fatalf("records=%v error=%#v, want search API error with request evidence", records, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = api.ListRecords(ctx, "app", "table", nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled ListRecords: calls=%d err=%v", calls, err)
	}
}
