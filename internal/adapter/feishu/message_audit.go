package feishu

import (
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strings"
)

var messageEmailPattern = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_{|}~-]+@[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+`)
var messageEmailLinkPattern = regexp.MustCompile(`(?i)(?:https?://|mailto:)[^\s<>)]*@|\]\([^)]*@`)

func IsContentAuditRejection(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr != nil && apiErr.Code == 230028
}

func withMessageEmailRedaction[T any](gatewayID, api, content string, call func(string) (T, error)) (T, error) {
	result, err := call(content)
	var apiErr *APIError
	if !IsContentAuditRejection(err) || !errors.As(err, &apiErr) || !strings.Contains(apiErr.Msg, "EMAIL_ADDRESS") {
		return result, err
	}
	redacted, ok := redactMessageEmails(content)
	if !ok {
		return result, err
	}
	// Remove rejected data, never disguise it or alter routing/callback fields.
	log.Printf("feishu message email audit redaction: gateway=%s api=%s", gatewayID, api)
	return call(redacted)
}

func redactMessageEmails(content string) (string, bool) {
	var payload any
	if json.Unmarshal([]byte(content), &payload) != nil {
		return "", false
	}
	changed := false
	unsafe := false
	var walk func(any)
	walk = func(node any) {
		switch node := node.(type) {
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			for key, value := range node {
				if key == "value" { // Interactive action values are machine data.
					continue
				}
				switch key {
				case "text", "content", "title", "description", "alt", "placeholder":
					if text, ok := value.(string); ok {
						if messageEmailPattern.MatchString(text) && messageEmailLinkPattern.MatchString(text) {
							unsafe = true
							continue
						}
						redacted := messageEmailPattern.ReplaceAllString(text, "[邮箱地址已移除]")
						if redacted != text {
							node[key] = redacted
							changed = true
						}
					}
				}
				walk(value)
			}
		}
	}
	walk(payload)
	if !changed || unsafe {
		return "", false
	}
	redacted, err := json.Marshal(payload)
	if err != nil || messageEmailPattern.Match(redacted) {
		return "", false
	}
	return string(redacted), true
}
