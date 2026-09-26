package feishu

import (
	"errors"
	"fmt"
	previewpkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/preview"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/xutil"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

type APIErrorDetail struct {
	Key   string
	Value string
}

type APIErrorPermissionViolation struct {
	Type        string
	Subject     string
	Description string
}

type APIErrorHelp struct {
	URL         string
	Description string
}

type APIError struct {
	API                  string
	Code                 int
	Msg                  string
	StatusCode           int
	RequestID            string
	LogID                string
	Troubleshooter       string
	RetryAfter           time.Duration
	RateLimitResetAfter  time.Duration
	Details              []APIErrorDetail
	PermissionViolations []APIErrorPermissionViolation
	Helps                []APIErrorHelp
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	api := strings.TrimSpace(e.API)
	if api == "" {
		api = "unknown"
	}
	msg := strings.TrimSpace(e.Msg)
	if msg == "" {
		return fmt.Sprintf("feishu api %s failed: code=%d", api, e.Code)
	}
	return fmt.Sprintf("feishu api %s failed: code=%d msg=%s", api, e.Code, msg)
}

type PermissionGapEvidence struct {
	Scope string
	// Scopes is an explicit any-of group; Scope remains its first member for older consumers.
	Scopes []string
	// UnresolvedPermissions preserves independent violations without inventing OR semantics.
	UnresolvedPermissions []string
	ScopeType             string
	ApplyURL              string
	ErrorCode             int
	ErrorMessage          string
	SourceAPI             string
	RequestID             string
}

type RateLimitEvidence struct {
	API                 string
	ErrorCode           int
	StatusCode          int
	RequestID           string
	RetryAfter          time.Duration
	RateLimitResetAfter time.Duration
}

var permissionScopePattern = regexp.MustCompile(`([a-z][a-z0-9_.-]*(?::[a-z0-9_.-]+)+)`)

func newAPIError(api string, resp *larkcore.ApiResp, codeErr larkcore.CodeError) *APIError {
	err := &APIError{
		API:  strings.TrimSpace(api),
		Code: codeErr.Code,
		Msg:  strings.TrimSpace(codeErr.Msg),
	}
	if resp != nil {
		err.StatusCode = resp.StatusCode
		err.RequestID = strings.TrimSpace(resp.RequestId())
		err.LogID = strings.TrimSpace(resp.LogId())
		err.RetryAfter = parseRetryAfterHeader(resp.Header)
		err.RateLimitResetAfter = parseRateLimitResetHeader(resp.Header)
	}
	if codeErr.Err == nil {
		return err
	}
	if strings.TrimSpace(codeErr.Err.LogID) != "" {
		err.LogID = strings.TrimSpace(codeErr.Err.LogID)
	}
	err.Troubleshooter = strings.TrimSpace(codeErr.Err.Troubleshooter)
	for _, item := range codeErr.Err.Details {
		if item == nil {
			continue
		}
		err.Details = append(err.Details, APIErrorDetail{
			Key:   strings.TrimSpace(item.Key),
			Value: strings.TrimSpace(item.Value),
		})
	}
	for _, item := range codeErr.Err.PermissionViolations {
		if item == nil {
			continue
		}
		err.PermissionViolations = append(err.PermissionViolations, APIErrorPermissionViolation{
			Type:        strings.TrimSpace(item.Type),
			Subject:     strings.TrimSpace(item.Subject),
			Description: strings.TrimSpace(item.Description),
		})
	}
	for _, item := range codeErr.Err.Helps {
		if item == nil {
			continue
		}
		err.Helps = append(err.Helps, APIErrorHelp{
			URL:         strings.TrimSpace(item.URL),
			Description: strings.TrimSpace(item.Description),
		})
	}
	return err
}

func ExtractPermissionGap(err error) (PermissionGapEvidence, bool) {
	var blockedErr *PermissionBlockedError
	if errors.As(err, &blockedErr) {
		return blockedErr.gap, blockedErr.gap.Scope != ""
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if gap, ok := permissionGapFromAPIError(apiErr); ok {
			return gap, true
		}
	}
	var driveErr *previewpkg.DriveAPIError
	if errors.As(err, &driveErr) {
		if gap, ok := permissionGapFromDriveAPIError(driveErr); ok {
			return gap, true
		}
	}
	return PermissionGapEvidence{}, false
}

func ExtractRateLimit(err error) (RateLimitEvidence, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return RateLimitEvidence{}, false
	}
	if apiErr.StatusCode != http.StatusTooManyRequests && apiErr.Code != 99991400 && apiErr.RetryAfter <= 0 && apiErr.RateLimitResetAfter <= 0 {
		return RateLimitEvidence{}, false
	}
	return RateLimitEvidence{
		API:                 strings.TrimSpace(apiErr.API),
		ErrorCode:           apiErr.Code,
		StatusCode:          apiErr.StatusCode,
		RequestID:           xutil.FirstNonEmpty(strings.TrimSpace(apiErr.RequestID), strings.TrimSpace(apiErr.LogID)),
		RetryAfter:          apiErr.RetryAfter,
		RateLimitResetAfter: apiErr.RateLimitResetAfter,
	}, true
}

func permissionGapFromAPIError(err *APIError) (PermissionGapEvidence, bool) {
	if err == nil {
		return PermissionGapEvidence{}, false
	}
	gap := PermissionGapEvidence{
		ErrorCode:    err.Code,
		ErrorMessage: strings.TrimSpace(err.Msg),
		SourceAPI:    strings.TrimSpace(err.API),
		RequestID:    xutil.FirstNonEmpty(strings.TrimSpace(err.RequestID), strings.TrimSpace(err.LogID)),
	}
	gap.Scopes = permissionAlternatives(err.Msg)
	if len(gap.Scopes) != 0 {
		gap.Scope = gap.Scopes[0]
	}
	if match := permissionMissingScopePattern.FindStringSubmatch(err.Msg); len(match) != 0 {
		gap.Scope = match[1]
	}
	for _, item := range err.PermissionViolations {
		if len(gap.Scopes) == 0 {
			if alternatives := permissionAlternatives(item.Description); len(alternatives) != 0 {
				gap.Scopes = alternatives
				gap.Scope = alternatives[0]
				gap.ScopeType = normalizePermissionScopeType(item.Type)
			}
		}
		if scope := normalizePermissionScope(item.Subject); scope != "" {
			if gap.Scope == "" {
				gap.Scope = scope
			}
			if gap.Scope == scope && gap.ScopeType == "" {
				gap.ScopeType = normalizePermissionScopeType(item.Type)
			}
		}
	}
	for _, item := range err.Details {
		switch strings.ToLower(strings.TrimSpace(item.Key)) {
		case "scope", "scope_name", "permission", "permission_scope":
			if gap.Scope == "" {
				gap.Scope = normalizePermissionScope(item.Value)
			}
		case "scope_type", "permission_type":
			if gap.ScopeType == "" {
				gap.ScopeType = normalizePermissionScopeType(item.Value)
			}
		}
	}
	gap.ApplyURL = firstPermissionURL(err)
	if gap.ScopeType == "" {
		if parsed, parseErr := url.Parse(gap.ApplyURL); parseErr == nil {
			gap.ScopeType = normalizePermissionScopeType(parsed.Query().Get("token_type"))
		}
	}
	if gap.Scope == "" {
		return PermissionGapEvidence{}, false
	}
	gap.UnresolvedPermissions = unresolvedPermissionViolations(gap, err.PermissionViolations)
	return gap, true
}

// Multiple structured violations are independent unless one explicit same-identity
// any-of group covers every subject and every stated alternative.
// ponytail: unresolved combinations stay blocked; add AND groups only with an upstream contract.
func unresolvedPermissionViolations(gap PermissionGapEvidence, violations []APIErrorPermissionViolation) []string {
	if len(violations) == 0 || (len(violations) == 1 && len(gap.Scopes) == 0) {
		return nil
	}
	covered := len(gap.Scopes) > 0 && gap.ScopeType != ""
	var evidence []string
	if len(gap.Scopes) != 0 {
		evidence = append(evidence, gap.ScopeType+": any of ["+strings.Join(gap.Scopes, ", ")+"]")
	}
	requestIdentity := ""
	if parsed, err := url.Parse(gap.ApplyURL); err == nil {
		requestIdentity = normalizePermissionScopeType(parsed.Query().Get("token_type"))
	}
	for _, item := range violations {
		identity := normalizePermissionScopeType(item.Type)
		if identity == "" {
			identity = requestIdentity
		}
		if identity != gap.ScopeType || !slices.Contains(gap.Scopes, normalizePermissionScope(item.Subject)) {
			covered = false
		}
		for _, scope := range permissionAlternatives(item.Description) {
			if !slices.Contains(gap.Scopes, scope) {
				covered = false
			}
		}
		description := strings.TrimSpace(item.Type) + ": " + strings.TrimSpace(item.Subject)
		if detail := strings.TrimSpace(item.Description); detail != "" {
			description += " (" + detail + ")"
		}
		evidence = append(evidence, description)
	}
	if covered {
		return nil
	}
	return evidence
}

func permissionGapFromDriveAPIError(err *previewpkg.DriveAPIError) (PermissionGapEvidence, bool) {
	if err == nil {
		return PermissionGapEvidence{}, false
	}
	return permissionGapFromAPIError(&APIError{
		API:  xutil.FirstNonEmpty(strings.TrimSpace(err.API), "drive.v1"),
		Code: err.Code, Msg: err.Msg, RequestID: err.RequestID, LogID: err.LogID,
	})
}

var permissionMissingScopePattern = regexp.MustCompile(`(?i)^\s*missing\s+([a-z][a-z0-9_.-]*(?::[a-z0-9_.-]+)+)\s*$`)

var permissionAnyOfPattern = regexp.MustCompile(`(?i)(?:one of the following scopes is required\s*[:：]\s*\[([^\]]+)\]|所需的(?:应用|用户)身份权限[：:]\s*\[([^\]]+)\][^。\n]*?(?:任一权限))`)
var permissionURLPattern = regexp.MustCompile(`https://open\.(?:feishu\.cn|larksuite\.com)/[^\s<>"，。]+`)

func permissionAlternatives(value string) []string {
	match := permissionAnyOfPattern.FindStringSubmatch(value)
	if len(match) == 0 {
		return nil
	}
	group := xutil.FirstNonEmpty(match[1], match[2])
	var scopes []string
	for _, value := range strings.Split(group, ",") {
		scope := normalizePermissionScope(value)
		if scope == "" {
			return nil
		}
		scopes = append(scopes, scope)
	}
	return scopes
}

func firstPermissionURL(err *APIError) string {
	if err == nil {
		return ""
	}
	var links []string
	for _, item := range err.Helps {
		if link := strings.TrimSpace(item.URL); link != "" {
			links = append(links, link)
		}
	}
	links = append(links, permissionURLPattern.FindAllString(err.Msg, -1)...)
	if link := strings.TrimSpace(err.Troubleshooter); link != "" {
		links = append(links, link)
	}
	for _, link := range links {
		if parsed, parseErr := url.Parse(link); parseErr == nil && normalizePermissionScopeType(parsed.Query().Get("token_type")) != "" {
			return link
		}
	}
	if len(links) != 0 {
		return links[0]
	}
	return ""
}

// PermissionGapSatisfied only clears a gap with known identity and an actually
// granted scope satisfying the captured API requirement (including explicit OR).
func PermissionGapSatisfied(gap PermissionGapEvidence, grants []AppScopeStatus) bool {
	if len(gap.UnresolvedPermissions) != 0 {
		return false
	}
	identity := normalizePermissionScopeType(gap.ScopeType)
	if identity != "tenant" && identity != "user" {
		return false
	}
	candidates := gap.Scopes
	if len(candidates) == 0 {
		candidates = []string{gap.Scope}
	}
	for _, scope := range candidates {
		if _, ok := MatchScopeRequirement(scope, identity, grants); ok {
			return true
		}
	}
	return false
}

func normalizePermissionScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if permissionScopePattern.FindString(value) == value {
		return value
	}
	return ""
}

func normalizePermissionScopeType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "tenant", "app", "tenant_access_token":
		return "tenant"
	case "user", "user_access_token":
		return "user"
	default:
		return ""
	}
}

func parseRetryAfterHeader(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	return parseDurationHeaderValue(headerValue(header, "Retry-After"), time.Now())
}

func parseRateLimitResetHeader(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	return parseDurationHeaderValue(headerValue(header, "x-ogw-ratelimit-reset"), time.Now())
}

func parseDurationHeaderValue(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(value, 64); err == nil {
		switch {
		case secs > 1_000_000_000_000:
			return positiveDuration(time.UnixMilli(int64(secs)).Sub(now))
		case secs > 1_000_000_000:
			return positiveDuration(time.Unix(int64(secs), 0).Sub(now))
		default:
			return positiveDuration(time.Duration(secs * float64(time.Second)))
		}
	}
	if ts, err := http.ParseTime(value); err == nil {
		return positiveDuration(ts.Sub(now))
	}
	return 0
}

func positiveDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func headerValue(header http.Header, key string) string {
	if header == nil {
		return ""
	}
	if value := header.Get(key); strings.TrimSpace(value) != "" {
		return value
	}
	for existingKey, values := range header {
		if !strings.EqualFold(existingKey, key) || len(values) == 0 {
			continue
		}
		if strings.TrimSpace(values[0]) != "" {
			return values[0]
		}
	}
	return ""
}
