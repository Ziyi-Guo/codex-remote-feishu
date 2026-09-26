package daemon

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/feishuapp"
)

var listFeishuAppGrantedScopes = feishu.ListAppGrantedScopes

type feishuPermissionGapRecord struct {
	Scope                 string
	Scopes                []string
	UnresolvedPermissions []string
	ScopeType             string
	ApplyURL              string
	LastErrorCode         int
	LastErrorMsg          string
	FirstSeenAt           time.Time
	LastSeenAt            time.Time
	HitCount              int
	LastSourceAPI         string
	LastRequestID         string
	LastVerifiedAt        time.Time
	LastVerifyError       string
}

func (a *App) observeFeishuPermissionError(gatewayID string, err error) bool {
	gatewayID = canonicalGatewayID(gatewayID)
	if gatewayID == "" {
		return false
	}
	gap, ok := feishu.ExtractPermissionGap(err)
	if !ok || strings.TrimSpace(gap.Scope) == "" {
		return false
	}
	now := time.Now().UTC()
	key := feishuPermissionGapKey(gap.Scope, gap.ScopeType) + "|" + gap.SourceAPI + "|" + strings.Join(gap.Scopes, ",") + "|" + strings.Join(gap.UnresolvedPermissions, "\n")
	a.feishuRuntime.permissionMu.Lock()
	defer a.feishuRuntime.permissionMu.Unlock()
	if a.feishuRuntime.permissionGaps[gatewayID] == nil {
		a.feishuRuntime.permissionGaps[gatewayID] = map[string]*feishuPermissionGapRecord{}
	}
	record := a.feishuRuntime.permissionGaps[gatewayID][key]
	if record == nil {
		record = &feishuPermissionGapRecord{
			Scope:                 strings.TrimSpace(gap.Scope),
			UnresolvedPermissions: append([]string(nil), gap.UnresolvedPermissions...),
			Scopes:                append([]string(nil), gap.Scopes...),
			ScopeType:             strings.TrimSpace(gap.ScopeType),
			ApplyURL:              strings.TrimSpace(gap.ApplyURL),
			FirstSeenAt:           now,
		}
		a.feishuRuntime.permissionGaps[gatewayID][key] = record
	}
	record.LastSeenAt = now
	record.HitCount++
	record.LastErrorCode = gap.ErrorCode
	record.LastErrorMsg = strings.TrimSpace(gap.ErrorMessage)
	record.LastSourceAPI = strings.TrimSpace(gap.SourceAPI)
	record.LastRequestID = strings.TrimSpace(gap.RequestID)
	if strings.TrimSpace(gap.ApplyURL) != "" {
		record.ApplyURL = strings.TrimSpace(gap.ApplyURL)
	}
	return true
}

func feishuPermissionGapKey(scope, scopeType string) string {
	return strings.TrimSpace(scope) + "|" + strings.TrimSpace(scopeType)
}

func (a *App) snapshotFeishuPermissionGaps(gatewayID string) []control.PermissionGapSummary {
	gatewayID = canonicalGatewayID(gatewayID)
	if gatewayID == "" {
		return nil
	}
	a.feishuRuntime.permissionMu.RLock()
	defer a.feishuRuntime.permissionMu.RUnlock()
	records := a.feishuRuntime.permissionGaps[gatewayID]
	if len(records) == 0 {
		return nil
	}
	values := make([]control.PermissionGapSummary, 0, len(records))
	for _, record := range records {
		if record == nil || strings.TrimSpace(record.Scope) == "" {
			continue
		}
		values = append(values, control.PermissionGapSummary{
			Scope:                 record.Scope,
			UnresolvedPermissions: append([]string(nil), record.UnresolvedPermissions...),
			Scopes:                append([]string(nil), record.Scopes...),
			ScopeType:             record.ScopeType,
			ApplyURL:              record.ApplyURL,
			SourceAPI:             record.LastSourceAPI,
			ErrorCode:             record.LastErrorCode,
			FirstSeenAt:           record.FirstSeenAt,
			LastSeenAt:            record.LastSeenAt,
			LastVerified:          record.LastVerifiedAt,
			HitCount:              record.HitCount,
		})
	}
	sort.Slice(values, func(i, j int) bool {
		if !values[i].LastSeenAt.Equal(values[j].LastSeenAt) {
			return values[i].LastSeenAt.After(values[j].LastSeenAt)
		}
		return values[i].Scope < values[j].Scope
	})
	return values
}

func (a *App) populateSnapshotFeishuPermissionGaps(snapshot *control.Snapshot, surfaceID string) {
	if snapshot == nil {
		return
	}
	gatewayID := a.service.SurfaceGatewayID(surfaceID)
	snapshot.PermissionGaps = a.snapshotFeishuPermissionGaps(gatewayID)
}

func (a *App) clearFeishuPermissionGaps(gatewayID string) {
	gatewayID = canonicalGatewayID(gatewayID)
	if gatewayID == "" {
		return
	}
	a.feishuRuntime.permissionMu.Lock()
	delete(a.feishuRuntime.permissionGaps, gatewayID)
	a.feishuRuntime.permissionMu.Unlock()
}

func (a *App) applyFeishuPermissionVerificationResult(gatewayID string, scopes []feishu.AppScopeStatus, err error) {
	gatewayID = canonicalGatewayID(gatewayID)
	if gatewayID == "" {
		return
	}
	now := time.Now().UTC()
	a.feishuRuntime.permissionMu.Lock()
	defer a.feishuRuntime.permissionMu.Unlock()
	records := a.feishuRuntime.permissionGaps[gatewayID]
	for key, record := range records {
		if record == nil {
			delete(records, key)
			continue
		}
		record.LastVerifiedAt = now
		record.LastVerifyError = ""
		if err != nil {
			record.LastVerifyError = err.Error()
			continue
		}
		if feishu.PermissionGapSatisfied(feishu.PermissionGapEvidence{Scope: record.Scope, Scopes: record.Scopes, ScopeType: record.ScopeType, UnresolvedPermissions: record.UnresolvedPermissions}, scopes) {
			delete(records, key)
		}
	}
	if len(records) == 0 {
		delete(a.feishuRuntime.permissionGaps, gatewayID)
	}
	if err != nil {
		log.Printf("feishu permission verification failed: gateway=%s err=%v", gatewayID, err)
		return
	}
	if clearer, ok := a.gateway.(feishu.PermissionBlockController); ok {
		clearer.ClearGrantedPermissionBlocks(gatewayID, scopes)
	}
}

func (a *App) CheckPrimaryBotPermission(ctx context.Context, req orchestrator.PrimaryBotPermissionRequest) orchestrator.PrimaryBotPermissionDecision {
	gatewayID := canonicalGatewayID(req.GatewayID)
	if gatewayID == "" {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "missing_gateway"}
	}
	if !req.ForceRefresh {
		if facts, ok := a.FeishuBotFacts(gatewayID); ok && feishuFactsScopesFresh(facts, time.Now().UTC()) {
			return primaryPermissionDecisionFromScopes(appScopesFromFeishuFactsScopes(facts.Scopes), nil)
		}
	}
	checkCtx := ctx
	if checkCtx == nil {
		checkCtx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(checkCtx, 20*time.Second)
	defer cancel()
	facts, err := a.RefreshFeishuBotFacts(checkCtx, gatewayID)
	return primaryPermissionDecisionFromScopes(appScopesFromFeishuFactsScopes(facts.Scopes), err)
}

func (a *App) checkFeishuScopePermission(ctx context.Context, gatewayID, feature string, forceRefresh bool) orchestrator.PrimaryBotPermissionDecision {
	gatewayID = canonicalGatewayID(gatewayID)
	feature = strings.TrimSpace(feature)
	if gatewayID == "" {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "missing_gateway"}
	}
	requirements := feishuScopeRequirementsByFeature(feature)
	if len(requirements) == 0 {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "scope_requirement_missing"}
	}
	if !forceRefresh {
		if facts, ok := a.FeishuBotFacts(gatewayID); ok && feishuFactsScopesFresh(facts, time.Now().UTC()) {
			return feishuScopePermissionDecisionFromScopes(requirements, appScopesFromFeishuFactsScopes(facts.Scopes), nil)
		}
	}
	checkCtx := ctx
	if checkCtx == nil {
		checkCtx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(checkCtx, 20*time.Second)
	defer cancel()
	facts, err := a.RefreshFeishuBotFacts(checkCtx, gatewayID)
	return feishuScopePermissionDecisionFromScopes(requirements, appScopesFromFeishuFactsScopes(facts.Scopes), err)
}

func primaryPermissionDecisionFromScopes(scopes []feishu.AppScopeStatus, err error) orchestrator.PrimaryBotPermissionDecision {
	if err != nil {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "scope_read_failed", Err: err}
	}
	requirement, ok := primaryPermissionScopeRequirement()
	if !ok {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "primary_scope_requirement_missing"}
	}
	if scope, ok := feishu.MatchScopeRequirement(requirement.Scope, requirement.ScopeType, scopes); ok {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: true, Scope: scope}
	}
	return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "missing_group_message_scope"}
}

func feishuScopePermissionDecisionFromScopes(requirements []feishuapp.ScopeRequirement, scopes []feishu.AppScopeStatus, err error) orchestrator.PrimaryBotPermissionDecision {
	if err != nil {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "scope_read_failed", Err: err}
	}
	if len(requirements) == 0 {
		return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "scope_requirement_missing"}
	}
	var firstScope string
	for _, requirement := range requirements {
		scope, ok := feishu.MatchScopeRequirement(requirement.Scope, requirement.ScopeType, scopes)
		if !ok {
			return orchestrator.PrimaryBotPermissionDecision{Allowed: false, Reason: "missing_scope"}
		}
		if firstScope == "" {
			firstScope = scope
		}
	}
	return orchestrator.PrimaryBotPermissionDecision{Allowed: true, Scope: firstScope}
}

func primaryPermissionScopeRequirement() (feishuapp.ScopeRequirement, bool) {
	requirements := feishuScopeRequirementsByFeature("primary_room_bot")
	if len(requirements) == 0 {
		return feishuapp.ScopeRequirement{}, false
	}
	return requirements[0], true
}

func feishuScopeRequirementsByFeature(feature string) []feishuapp.ScopeRequirement {
	feature = strings.TrimSpace(feature)
	var requirements []feishuapp.ScopeRequirement
	for _, requirement := range feishuapp.DefaultManifest().ScopeRequirements {
		if requirement.Required && strings.TrimSpace(requirement.Feature) == feature {
			requirements = append(requirements, requirement)
		}
	}
	return requirements
}
