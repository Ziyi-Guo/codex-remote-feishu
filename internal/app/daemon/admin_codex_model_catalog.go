package daemon

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type codexModelCatalogResponse struct {
	Entries    []agentproto.ModelCatalogEntry `json:"entries"`
	Validation string                         `json:"validation"`
	Message    string                         `json:"message,omitempty"`
}

// Caller holds a.mu. A failing or incomplete catalog cannot prove absence.
func (a *App) codexNativeCatalogsLocked() ([]*agentproto.ModelCatalogSnapshot, bool) {
	var catalogs []*agentproto.ModelCatalogSnapshot
	unknown := false
	for _, inst := range a.service.Instances() {
		if !inst.Online || inst.Backend != agentproto.BackendCodex || inst.Source != "headless" {
			continue
		}
		native := inst.CodexProfileID == state.NativeCodexProfileID || inst.CodexProfileID == state.OAuthCodexProfileID
		if c := inst.CodexConnectionContract; c != nil {
			native = c.Kind == state.CodexProfileKindNative || c.Kind == state.CodexProfileKindOAuth
		}
		if !native {
			continue
		}
		c := inst.ModelCatalog
		if c == nil || c.Unsupported || c.ErrorMessage != "" || c.NextCursor != "" || len(c.Entries) == 0 {
			unknown = true
			continue
		}
		catalogs = append(catalogs, c)
	}
	return catalogs, unknown || len(catalogs) == 0
}

func catalogModel(entry agentproto.ModelCatalogEntry) string {
	if model := strings.TrimSpace(entry.Model); model != "" {
		return model
	}
	return strings.TrimSpace(entry.ID)
}

func (a *App) validateCodexModelLocked(model, effort string) (string, string, error) {
	if model == "" {
		return "verified", "", nil
	}
	catalogs, unknown := a.codexNativeCatalogsLocked()
	for _, catalog := range catalogs {
		found := false
		for _, entry := range catalog.Entries {
			if catalogModel(entry) != model {
				continue
			}
			found = true
			if len(entry.SupportedReasoningEfforts) == 0 {
				unknown = true
				break
			}
			supported := false
			for _, option := range entry.SupportedReasoningEfforts {
				if option.ReasoningEffort == effort {
					supported = true
					break
				}
			}
			if !supported {
				return "", "", fmt.Errorf("当前在线 Codex 实例的模型目录不支持 %s / %s；运行中的配置未改变", model, effort)
			}
			break
		}
		if !found {
			return "", "", fmt.Errorf("当前在线 Codex 实例的完整模型目录没有 %s；运行中的配置未改变", model)
		}
	}
	if unknown {
		return "unverified", "部分模型目录不可用或不完整，模型可用性尚未验证。", nil
	}
	return "verified", "已按当前在线 Codex 模型目录校验；实际请求仍取决于运行时可用性。", nil
}

func (a *App) handleCodexModelCatalog(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	catalogs, unknown := a.codexNativeCatalogsLocked()
	models := map[string]agentproto.ModelCatalogEntry{}
	for index, catalog := range catalogs {
		current := map[string]agentproto.ModelCatalogEntry{}
		for _, entry := range catalog.Entries {
			model := catalogModel(entry)
			if model == "" || entry.Hidden {
				continue
			}
			if len(entry.SupportedReasoningEfforts) == 0 {
				unknown = true
			}
			current[model] = agentproto.ModelCatalogEntry{Model: model, DisplayName: entry.DisplayName, SupportedReasoningEfforts: append([]agentproto.ReasoningEffortOption(nil), entry.SupportedReasoningEfforts...)}
		}
		if index == 0 {
			models = current
			continue
		}
		for model, previous := range models {
			entry, exists := current[model]
			if !exists {
				delete(models, model)
				continue
			}
			// An unspecified effort list is unknown, not evidence of exclusion.
			if len(previous.SupportedReasoningEfforts) == 0 {
				models[model] = entry
				continue
			}
			if len(entry.SupportedReasoningEfforts) == 0 {
				continue
			}
			var common []agentproto.ReasoningEffortOption
			for _, option := range previous.SupportedReasoningEfforts {
				for _, candidate := range entry.SupportedReasoningEfforts {
					if option.ReasoningEffort == candidate.ReasoningEffort {
						common = append(common, option)
						break
					}
				}
			}
			if len(common) == 0 {
				delete(models, model)
				continue
			}
			previous.SupportedReasoningEfforts = common
			models[model] = previous
		}
	}
	response := codexModelCatalogResponse{Entries: make([]agentproto.ModelCatalogEntry, 0, len(models)), Validation: "verified"}
	if unknown {
		response.Validation = "unverified"
		response.Message = "部分模型目录不可用或不完整，模型可用性尚未验证。"
	}
	for _, entry := range models {
		response.Entries = append(response.Entries, entry)
	}
	sort.Slice(response.Entries, func(i, j int) bool { return response.Entries[i].Model < response.Entries[j].Model })
	writeJSON(w, http.StatusOK, response)
}
