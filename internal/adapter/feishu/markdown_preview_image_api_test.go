package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestLarkPreviewImageUploadUsesGatewayIMAPI(t *testing.T) {
	want := []byte("test image content")
	var imageHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case larkcore.TenantAccessTokenInternalUrlPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "test-gateway-token", "expire": 7200})
		case "/open-apis/im/v1/images":
			imageHits++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-gateway-token" {
				t.Errorf("unexpected gateway request: method=%s auth=%q", r.Method, r.Header.Get("Authorization"))
			}
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("image_type") != "message" {
				t.Errorf("image_type=%q", r.FormValue("image_type"))
			}
			file, _, err := r.FormFile("image")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("image bytes=%q err=%v", got, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"image_key": "img_v3_gateway-key"}})
		default:
			t.Errorf("unexpected API: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := lark.NewClient("preview_image_test_app", "test_secret", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
	api := NewLarkDrivePreviewAPI("gateway-image-test", client)
	uploader, ok := api.(interface {
		UploadImage(context.Context, []byte) (string, error)
	})
	if !ok {
		t.Fatal("gateway preview API cannot upload IM images")
	}
	key, err := uploader.UploadImage(context.Background(), want)
	if err != nil || key != "img_v3_gateway-key" || imageHits != 1 {
		t.Fatalf("key=%q hits=%d err=%v", key, imageHits, err)
	}
}
