package preview

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// IsCardImageKey rejects URLs and local paths: card markdown accepts an IM image key.
func IsCardImageKey(target string) bool {
	if !strings.HasPrefix(target, "img_") || len(target) <= len("img_") {
		return false
	}
	for _, r := range target[len("img_"):] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func RenderCardImageFallback(label, target string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "图片"
	}
	label += "（图片未能内嵌）"
	target = normalizeStandalonePreviewDisplay(target)
	if parsed, err := url.Parse(target); err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
		return "[" + label + "](" + target + ")"
	}
	if strings.Contains(target, ":") && !looksLikeDelimitedPreviewTarget(target) {
		return label
	}
	fence := "`"
	for strings.Contains(target, fence) {
		fence += "`"
	}
	return label + " (" + fence + target + fence + ")"
}

func (p *DriveMarkdownPreviewer) rewriteMarkdownImage(ctx context.Context, req FinalBlockPreviewRequest, principals []previewPrincipal, runtime *previewRewriteRuntime, scopeKey string, targets map[string]string, label, target string, offset int) (string, []string) {
	if IsCardImageKey(target) {
		return "![" + label + "](" + target + ")", nil
	}
	cacheKey := "image:" + target
	if cached, ok := targets[cacheKey]; ok {
		if IsCardImageKey(cached) {
			return "![" + label + "](" + cached + ")", nil
		}
		return RenderCardImageFallback(label, cached), nil
	}
	ref := PreviewReference{RawTarget: target, Image: true, TargetStart: offset + len(label) + 4, TargetEnd: offset + len(label) + 4 + len(target)}
	published, ok, err := p.materializePreviewTarget(ctx, ref, req, scopeKey, principals, runtime)
	var errs []string
	if err != nil {
		errs = append(errs, err.Error())
	}
	if ok && published != nil {
		if published.Mode == PreviewPublishModeInlineImage && IsCardImageKey(published.ImageKey) {
			targets[cacheKey] = published.ImageKey
			return "![" + label + "](" + published.ImageKey + ")", errs
		}
		if published.Mode == PreviewPublishModeInlineLink && published.URL != "" {
			target = published.URL
		}
	}
	targets[cacheKey] = target
	return RenderCardImageFallback(label, target), errs
}

type previewImageUploader interface {
	UploadImage(context.Context, []byte) (string, error)
}

type imImagePreviewPublisher struct{ previewer *DriveMarkdownPreviewer }

func (p imImagePreviewPublisher) ID() string { return "im_image" }

func (p imImagePreviewPublisher) Supports(delivery PreviewDeliveryPlan, artifact PreparedPreviewArtifact) bool {
	return p.previewer != nil && delivery.Kind == PreviewDeliveryIMImage && artifact.ArtifactKind == "image"
}

func (p imImagePreviewPublisher) Publish(ctx context.Context, req PreviewPublishRequest) (*PreviewPublishResult, bool, error) {
	artifact := req.Plan.Artifact
	uploader, ok := p.previewer.api.(previewImageUploader)
	if !ok {
		return nil, false, fmt.Errorf("image preview upload unavailable")
	}
	switch http.DetectContentType(artifact.Bytes) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, false, fmt.Errorf("image preview source is not a supported raster: %s", artifact.SourcePath)
	}
	if len(artifact.Bytes) > 10*1024*1024 {
		return nil, false, fmt.Errorf("image preview source exceeds 10 MiB: %s", artifact.SourcePath)
	}
	key := previewFileKey(req.ScopeKey, artifact.SourcePath, artifact.ContentHash)
	value, err := p.previewer.doPreviewOp("im-image:"+key, func() (any, error) {
		p.previewer.stateMu.Lock()
		cached := p.previewer.imageKeys[key]
		p.previewer.stateMu.Unlock()
		if cached != "" {
			return cached, nil
		}
		imageKey, err := uploader.UploadImage(ctx, artifact.Bytes)
		if err != nil {
			return nil, fmt.Errorf("upload image preview for %s: %w", artifact.SourcePath, err)
		}
		if !IsCardImageKey(imageKey) {
			return nil, fmt.Errorf("image preview upload returned invalid image key")
		}
		p.previewer.stateMu.Lock()
		if p.previewer.imageKeys == nil {
			p.previewer.imageKeys = map[string]string{}
		}
		p.previewer.imageKeys[key] = imageKey
		p.previewer.stateMu.Unlock()
		return imageKey, nil
	})
	if err != nil {
		return nil, false, err
	}
	return &PreviewPublishResult{PublisherID: p.ID(), Mode: PreviewPublishModeInlineImage, ImageKey: value.(string)}, true, nil
}
