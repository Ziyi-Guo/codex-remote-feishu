package preview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

type fakeImagePreviewAPI struct {
	*fakePreviewAPI
	imageMu  sync.Mutex
	images   [][]byte
	imageKey string
	imageErr error
}

func (f *fakeImagePreviewAPI) UploadImage(_ context.Context, content []byte) (string, error) {
	f.imageMu.Lock()
	defer f.imageMu.Unlock()
	f.images = append(f.images, append([]byte(nil), content...))
	return f.imageKey, f.imageErr
}

func writePreviewPNG(t *testing.T, path string) []byte {
	t.Helper()
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}

func imagePreviewRequest(root, text string) FinalBlockPreviewRequest {
	return FinalBlockPreviewRequest{
		GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", ThreadCWD: root,
		Block: render.Block{Kind: render.BlockAssistantMarkdown, Final: true, Text: text},
	}
}

func TestMarkdownPreviewImagesUploadAndDeduplicate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "生成 result.png")
	content := writePreviewPNG(t, path)
	api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageKey: "img_v3_uploaded-key"}
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root})
	text := "前文 ![结果](<" + path + ">) 后文 ![第二张](<" + path + ">)\n`![代码](" + path + ")`\n```md\n![示例](" + path + ")\n```"
	want := strings.ReplaceAll(strings.ReplaceAll(text, "![结果](<"+path+">)", "![结果](img_v3_uploaded-key)"), "![第二张](<"+path+">)", "![第二张](img_v3_uploaded-key)")
	for i := 0; i < 2; i++ {
		result, err := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, text))
		if err != nil {
			t.Fatal(err)
		}
		if result.Block.Text != want {
			t.Fatalf("image body = %q, want %q", result.Block.Text, want)
		}
	}
	if len(api.images) != 1 || !bytes.Equal(api.images[0], content) {
		t.Fatalf("uploads = %#v", api.images)
	}
}

func TestMarkdownPreviewImageFailurePreservesAnswer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.png")
	writePreviewPNG(t, path)
	for _, tc := range []struct {
		name, key string
		err       error
	}{
		{"upload failure", "", errors.New("upload denied")},
		{"invalid key", "https://example.com/result.png", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageKey: tc.key, imageErr: tc.err}
			p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root})
			result, _ := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, "前文 ![结果]("+path+") 后文"))
			if strings.Contains(result.Block.Text, "![") || !strings.Contains(result.Block.Text, "前文") || !strings.Contains(result.Block.Text, "后文") || !strings.Contains(result.Block.Text, "结果") {
				t.Fatalf("unsafe or discarded answer: %q", result.Block.Text)
			}
			if !strings.Contains(result.Block.Text, "图片未能内嵌") {
				t.Fatalf("missing failure explanation: %q", result.Block.Text)
			}
		})
	}
}

func TestMarkdownPreviewImageAuthorizationAndUnsupportedTargets(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.png")
	writePreviewPNG(t, outside)
	bad := filepath.Join(root, "not-image.png")
	if err := os.WriteFile(bad, []byte("secret plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageKey: "img_v3_uploaded-key"}
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root})
	for _, target := range []string{outside, bad, "https://example.com/result.png", "data:image/png;base64,abc"} {
		result, _ := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, "前文 ![结果]("+target+") 后文"))
		if strings.Contains(result.Block.Text, "![") {
			t.Fatalf("unsupported image survived: %q", result.Block.Text)
		}
		if !strings.Contains(result.Block.Text, "结果") {
			t.Fatalf("lost image description: %q", result.Block.Text)
		}
	}
	if len(api.images) != 0 {
		t.Fatalf("unauthorized or non-raster upload: %#v", api.images)
	}
}

func TestMarkdownPreviewImageFailureUsesFileLink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.png")
	writePreviewPNG(t, path)
	api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageErr: errors.New("upload denied")}
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root, CacheDir: filepath.Join(root, "cache")})
	p.SetWebPreviewPublisher(&fakeWebPreviewPublisher{baseURL: "http://127.0.0.1:9501/preview/s/shared/?t=token"})
	result, err := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, "前文 ![结果]("+path+") 后文"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Block.Text, "[结果（图片未能内嵌）](http://127.0.0.1:9501/preview/s/shared/") || strings.Contains(result.Block.Text, "![") {
		t.Fatalf("image fallback = %q", result.Block.Text)
	}
}

func TestMarkdownPreviewImageAndFileReferenceKeepSeparateDelivery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.png")
	writePreviewPNG(t, path)
	api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageKey: "img_v3_uploaded-key"}
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root, CacheDir: filepath.Join(root, "cache")})
	p.SetWebPreviewPublisher(&fakeWebPreviewPublisher{baseURL: "https://preview.example/s/shared/?t=token"})
	result, err := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, "![结果]("+path+") [文件]("+path+")"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Block.Text, "![结果](img_v3_uploaded-key)") || !strings.Contains(result.Block.Text, "[文件](https://preview.example/s/shared/") {
		t.Fatalf("mixed references = %q", result.Block.Text)
	}
}

func TestMarkdownPreviewConcurrentImageUpload(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.png")
	writePreviewPNG(t, path)
	api := &fakeImagePreviewAPI{fakePreviewAPI: newFakePreviewAPI(), imageKey: "img_v3_uploaded-key"}
	p := NewDriveMarkdownPreviewer(api, MarkdownPreviewConfig{ProcessCWD: root})
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := p.RewriteFinalBlock(context.Background(), imagePreviewRequest(root, "![结果]("+path+")"))
			if err != nil || result.Block.Text != "![结果](img_v3_uploaded-key)" {
				t.Errorf("result=%q err=%v", result.Block.Text, err)
			}
		}()
	}
	group.Wait()
	if len(api.images) != 1 {
		t.Fatalf("concurrent uploads=%d", len(api.images))
	}
}
