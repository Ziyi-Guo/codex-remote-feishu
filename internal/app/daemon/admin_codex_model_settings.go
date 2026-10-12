package daemon

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type codexModelSettingsApplyResponse struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	ConfigPath      string `json:"configPath"`
	Applied         bool   `json:"applied"`
	Validation      string `json:"validation"`
	Message         string `json:"message,omitempty"`
}

func (a *App) publishCodexModelSettingsLocked(cfg config.CodexSettings) {
	presets := map[string]state.CodexPromptOverrideRecord{}
	for key, value := range config.EffectiveCodexModelPresets(cfg.ModelPresets) {
		presets[key] = state.CodexPromptOverrideRecord{Model: value.Model, ReasoningEffort: value.ReasoningEffort}
	}
	a.service.SetCodexRemoteDefault(cfg.DefaultModel, cfg.DefaultReasoningEffort)
	a.service.SetCodexModelPresets(presets)
	a.codexModelSettingsInitialized = true
}

func canonicalModelConfigPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func (a *App) validateCodexModelSettingsLocked(cfg config.CodexSettings) (string, string, error) {
	validation, message, err := a.validateCodexModelLocked(cfg.DefaultModel, cfg.DefaultReasoningEffort)
	if err != nil {
		return "", "", err
	}
	keys := make([]string, 0, len(cfg.ModelPresets))
	for key := range cfg.ModelPresets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		preset := cfg.ModelPresets[key]
		status, warning, err := a.validateCodexModelLocked(preset.Model, preset.ReasoningEffort)
		if err != nil {
			return "", "", fmt.Errorf("前缀 [%s]：%w", key, err)
		}
		if status == "unverified" {
			validation, message = status, warning
		}
	}
	return validation, message, nil
}

func (a *App) handleCodexModelSettingsApply(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConfigPath string `json:"configPath"`
	}
	if err := decodeJSONBody(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "failed to decode config apply request"})
		return
	}
	a.adminConfigMu.Lock()
	defer a.adminConfigMu.Unlock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "config_invalid", Message: "配置文件无效，运行中的配置未改变", Details: err.Error()})
		return
	}
	if _, err := os.Stat(loaded.Path); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "config_unavailable", Message: "配置文件不存在或不可读，运行中的配置未改变"})
		return
	}
	path, err := canonicalModelConfigPath(loaded.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "config_path_invalid", Message: "无法解析服务配置文件路径"})
		return
	}
	if request.ConfigPath != "" {
		requested, err := canonicalModelConfigPath(request.ConfigPath)
		if err != nil || requested != path {
			writeAPIError(w, http.StatusConflict, apiError{Code: "config_path_mismatch", Message: "请求的配置文件与此服务的配置文件不一致，未应用"})
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	validation, message, err := a.validateCodexModelSettingsLocked(loaded.Config.Codex)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "codex_model_unavailable", Message: err.Error()})
		return
	}
	a.publishCodexModelSettingsLocked(loaded.Config.Codex)
	writeJSON(w, http.StatusOK, codexModelSettingsApplyResponse{Model: loaded.Config.Codex.DefaultModel, ReasoningEffort: loaded.Config.Codex.DefaultReasoningEffort, ConfigPath: path, Applied: true, Validation: validation, Message: message})
}
