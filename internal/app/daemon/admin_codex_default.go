package daemon

import (
	"net/http"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

type codexRemoteDefaultResponse struct {
	Model                  string `json:"model"`
	ReasoningEffort        string `json:"reasoningEffort"`
	AppliedModel           string `json:"appliedModel"`
	AppliedReasoningEffort string `json:"appliedReasoningEffort"`
	PendingChanges         bool   `json:"pendingChanges"`
	Applied                bool   `json:"applied"`
	Validation             string `json:"validation"`
	Message                string `json:"message,omitempty"`
}

func (a *App) codexDefaultResponseLocked(model, effort, validation, message string) codexRemoteDefaultResponse {
	applied := a.service.CodexRemoteDefault()
	pending := model != applied.Model || effort != applied.ReasoningEffort
	return codexRemoteDefaultResponse{Model: model, ReasoningEffort: effort, AppliedModel: applied.Model, AppliedReasoningEffort: applied.ReasoningEffort, PendingChanges: pending, Applied: !pending, Validation: validation, Message: message}
}

func (a *App) handleCodexRemoteDefaultGet(w http.ResponseWriter, _ *http.Request) {
	a.adminConfigMu.Lock()
	defer a.adminConfigMu.Unlock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiError{Code: "config_unavailable", Message: "failed to load config", Details: err.Error()})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	model, effort := loaded.Config.Codex.DefaultModel, loaded.Config.Codex.DefaultReasoningEffort
	validation, message, err := a.validateCodexModelLocked(model, effort)
	if err != nil {
		validation, message = "unverified", err.Error()
	}
	writeJSON(w, http.StatusOK, a.codexDefaultResponseLocked(model, effort, validation, message))
}

func (a *App) handleCodexRemoteDefaultPut(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if err := decodeJSONBody(r, &value); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "failed to decode default model"})
		return
	}
	value.Model, value.ReasoningEffort = strings.TrimSpace(value.Model), strings.TrimSpace(value.ReasoningEffort)
	if err := config.ValidateCodexRemoteDefault(value.Model, value.ReasoningEffort); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "codex_default_invalid", Message: err.Error()})
		return
	}
	a.adminConfigMu.Lock()
	defer a.adminConfigMu.Unlock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiError{Code: "config_unavailable", Message: "failed to load config", Details: err.Error()})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	validation, message, err := a.validateCodexModelLocked(value.Model, value.ReasoningEffort)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{Code: "codex_model_unavailable", Message: err.Error()})
		return
	}
	loaded.Config.Codex.DefaultModel = value.Model
	loaded.Config.Codex.DefaultReasoningEffort = value.ReasoningEffort
	if err := config.WriteAppConfig(loaded.Path, loaded.Config); err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiError{Code: "config_write_failed", Message: "failed to save default model", Details: err.Error()})
		return
	}
	// A default-only save must not apply unrelated pending preset edits from disk.
	a.service.SetCodexRemoteDefault(value.Model, value.ReasoningEffort)
	writeJSON(w, http.StatusOK, a.codexDefaultResponseLocked(value.Model, value.ReasoningEffort, validation, message))
}
