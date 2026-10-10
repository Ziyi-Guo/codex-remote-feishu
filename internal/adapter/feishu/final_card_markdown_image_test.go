package feishu

import (
	"strings"
	"testing"
)

func TestRenderFinalCardMarkdownImages(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"uploaded key", "前文 ![结果](img_v3_uploaded-key) 后文", "前文 ![结果](img_v3_uploaded-key) 后文"},
		{"remote URL", "前文 ![结果](https://example.com/result.png) 后文", "前文 [结果（图片未能内嵌）](https://example.com/result.png) 后文"},
		{"local path", "前文 ![结果](/tmp/result.png) 后文", "前文 结果（图片未能内嵌） (`/tmp/result.png`) 后文"},
		{"code", "`![结果](/tmp/result.png)`\n```md\n![结果](https://example.com/result.png)\n```", "`![结果](/tmp/result.png)`\n```md\n![结果](https://example.com/result.png)\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFinalCardMarkdown(tc.input); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderFinalCardMarkdownImageWithoutPreviewService(t *testing.T) {
	body := renderFinalCardMarkdown("回答 ![外链](http://127.0.0.1:9501/preview/s/file.png)\n![](/tmp/result.png)")
	if strings.Contains(body, "![") || !strings.Contains(body, "回答") || !strings.Contains(body, "图片未能内嵌") {
		t.Fatalf("unsafe image markdown: %q", body)
	}
}
