package feishu

import (
	"context"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type cachedFeishuToken struct {
	value   string
	expires time.Time
}

type resettableFeishuTokenCache struct{ entries sync.Map }

func (c *resettableFeishuTokenCache) Get(_ context.Context, key string) (string, error) {
	if value, ok := c.entries.Load(key); ok {
		entry := value.(cachedFeishuToken)
		if time.Now().Before(entry.expires) {
			return entry.value, nil
		}
	}
	return "", nil
}

func (c *resettableFeishuTokenCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	c.entries.Store(key, cachedFeishuToken{value: value, expires: time.Now().Add(ttl)})
	return nil
}

func (c *resettableFeishuTokenCache) clear() {
	// ponytail: clear all app tokens on rejection; use per-app eviction if auth churn becomes frequent.
	c.entries.Range(func(key, _ any) bool { c.entries.Delete(key); return true })
}

var feishuTokenCache = &resettableFeishuTokenCache{}

const (
	defaultLarkRequestTimeout            = 2 * time.Minute
	sendIMFileTimeout                    = 2 * time.Minute
	inboundMessageParseTimeout           = 30 * time.Second
	asyncInboundFailureNoticeTimeout     = 10 * time.Second
	previewDriveSummaryTimeout           = 20 * time.Second
	previewDriveCleanupTimeout           = 45 * time.Second
	previewDriveBackgroundCleanupTimeout = 45 * time.Second
)

func NewLarkClient(appID, appSecret string, options ...lark.ClientOptionFunc) *lark.Client {
	clientOptions := []lark.ClientOptionFunc{
		lark.WithReqTimeout(defaultLarkRequestTimeout),
		lark.WithTokenCache(feishuTokenCache),
	}
	clientOptions = append(clientOptions, options...)
	return lark.NewClient(
		strings.TrimSpace(appID),
		strings.TrimSpace(appSecret),
		clientOptions...,
	)
}

func NewLarkClientWithOpenBaseURL(appID, appSecret, openBaseURL string) *lark.Client {
	var options []lark.ClientOptionFunc
	if openBaseURL = strings.TrimSpace(openBaseURL); openBaseURL != "" {
		options = append(options, lark.WithOpenBaseUrl(openBaseURL))
	}
	return NewLarkClient(appID, appSecret, options...)
}
