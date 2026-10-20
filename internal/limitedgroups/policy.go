package limitedgroups

import (
	"errors"
	"path/filepath"
	"regexp"
	"time"
)

var ErrPolicy = errors.New("invalid limited-group policy")
var principalID = regexp.MustCompile(`^(oc|ou)_[A-Za-z0-9_-]{1,128}$`)

// Policy applies only to its exact chat. It never replaces a gateway's ordinary
// personal conversations or grants a colleague authority to approve execution.
type Policy struct {
	ExecutionMode      string         `json:"executionMode,omitempty"`
	Enabled            bool           `json:"enabled"`
	ChatID             string         `json:"chatId"`
	OwnerUserID        string         `json:"ownerUserId"`
	AllowedUserIDs     []string       `json:"allowedUserIds"`
	Workspace          string         `json:"workspace"`
	DailyLimit         int            `json:"dailyLimit"`
	UserDailyLimits    map[string]int `json:"userDailyLimits,omitempty"`
	Timezone           string         `json:"timezone,omitempty"`
	ExecutionServers   []ToolServer   `json:"executionServers,omitempty"`
	NativeConfigPath   string         `json:"nativeConfigPath,omitempty"`
	NativeConfigSHA256 string         `json:"nativeConfigSha256,omitempty"`
}

func (p Policy) Validate() error {
	if !validPrincipal(p.ChatID, "oc_") {
		return ErrPolicy
	}
	if !p.Enabled {
		return nil
	}
	if !validExecutionMode(p.ExecutionMode) {
		return ErrPolicy
	}
	if !validPrincipal(p.OwnerUserID, "ou_") || !filepath.IsAbs(p.Workspace) || filepath.Clean(p.Workspace) == filepath.VolumeName(p.Workspace)+string(filepath.Separator) || p.DailyLimit < 1 || p.DailyLimit > 10000 || len(p.AllowedUserIDs) == 0 || len(p.AllowedUserIDs) > 100 {
		return ErrPolicy
	}
	seen := map[string]bool{}
	for _, id := range p.AllowedUserIDs {
		if !validPrincipal(id, "ou_") || seen[id] {
			return ErrPolicy
		}
		seen[id] = true
	}
	if !seen[p.OwnerUserID] {
		return ErrPolicy
	}
	for id, n := range p.UserDailyLimits {
		if !seen[id] || n < 1 || n > 10000 {
			return ErrPolicy
		}
	}
	if _, err := time.LoadLocation(p.LocationName()); err != nil {
		return ErrPolicy
	}
	return nil
}

func (p Policy) Allows(chatID, userID string) bool {
	if !p.Enabled || chatID != p.ChatID || p.Validate() != nil {
		return false
	}
	for _, id := range p.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (p Policy) Limit(userID string) int {
	if n, ok := p.UserDailyLimits[userID]; ok {
		return n
	}
	return p.DailyLimit
}

func (p Policy) LocationName() string {
	if p.Timezone != "" {
		return p.Timezone
	}
	return "Asia/Shanghai"
}

func validPrincipal(id, prefix string) bool {
	return len(id) > len(prefix) && id[:len(prefix)] == prefix && principalID.MatchString(id)
}
