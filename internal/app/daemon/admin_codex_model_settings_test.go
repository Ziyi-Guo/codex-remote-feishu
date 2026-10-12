package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func addModelSettingsCatalog(app *App, model string, efforts ...string) {
	options := make([]agentproto.ReasoningEffortOption, 0, len(efforts))
	for _, effort := range efforts {
		options = append(options, agentproto.ReasoningEffortOption{ReasoningEffort: effort})
	}
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "models-native", Backend: agentproto.BackendCodex, Online: true, Source: "headless", CodexProfileID: state.NativeCodexProfileID, ModelCatalog: &agentproto.ModelCatalogSnapshot{Entries: []agentproto.ModelCatalogEntry{{Model: model, SupportedReasoningEfforts: options}}}})
}

func TestModelSettingsApplyPublishesOnlyExplicitValidChanges(t *testing.T) {
	cfg := config.DefaultAppConfig()
	cfg.Codex.DefaultModel, cfg.Codex.DefaultReasoningEffort = "old-model", "high"
	app, path := newFeishuAdminTestApp(t, cfg, defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "new-model", "high")
	cfg.Codex.DefaultModel = "new-model"
	cfg.Codex.ModelPresets = map[string]config.CodexModelPreset{"sol": {Model: "new-model", ReasoningEffort: "high"}}
	if err := config.WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	app.syncCodexProfilesCatalogFromConfig()
	if got := app.service.CodexRemoteDefault().Model; got != "old-model" {
		t.Fatalf("unrelated profile refresh applied pending file: %s", got)
	}
	get := performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/default-model", "")
	var pending codexRemoteDefaultResponse
	if err := json.Unmarshal(get.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.PendingChanges || pending.AppliedModel != "old-model" {
		t.Fatalf("pending state: %s", get.Body.String())
	}
	body, _ := json.Marshal(map[string]string{"configPath": path})
	res := performAdminRequest(t, app, http.MethodPost, "/api/admin/codex/model-settings/apply", string(body))
	if res.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", res.Code, res.Body.String())
	}
	if got := app.service.CodexRemoteDefault().Model; got != "new-model" {
		t.Fatal(got)
	}
	if got := app.service.CodexModelPresets()["sol"].Model; got != "new-model" {
		t.Fatal(got)
	}
	if !strings.Contains(res.Body.String(), `"validation":"verified"`) {
		t.Fatal(res.Body.String())
	}
}

func TestModelSettingsFailedApplyPreservesRuntime(t *testing.T) {
	cfg := config.DefaultAppConfig()
	cfg.Codex.DefaultModel = "old-model"
	cfg.Codex.DefaultReasoningEffort = "high"
	app, path := newFeishuAdminTestApp(t, cfg, defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "old-model", "high")
	cfg.Codex.DefaultModel = "unsupported-model"
	if err := config.WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"configPath":"/another/config.json"}`} {
		res := performAdminRequest(t, app, http.MethodPost, "/api/admin/codex/model-settings/apply", body)
		if res.Code == http.StatusOK {
			t.Fatalf("unexpected success: %s", res.Body.String())
		}
		if app.service.CodexRemoteDefault().Model != "old-model" {
			t.Fatal("runtime changed")
		}
	}
	if err := os.WriteFile(path, []byte(`{"codex":`), 0600); err != nil {
		t.Fatal(err)
	}
	res := performAdminRequest(t, app, http.MethodPost, "/api/admin/codex/model-settings/apply", `{}`)
	if res.Code == http.StatusOK || app.service.CodexRemoteDefault().Model != "old-model" {
		t.Fatalf("bad JSON applied: %s", res.Body.String())
	}
}

func TestDefaultPutRejectsKnownUnsupportedWithoutPersisting(t *testing.T) {
	app, path := newFeishuAdminTestApp(t, config.DefaultAppConfig(), defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "gpt-6.1-sol", "high")
	before, _ := os.ReadFile(path)
	for _, body := range []string{`{"model":"absent","reasoningEffort":"high"}`, `{"model":"gpt-6.1-sol","reasoningEffort":"low"}`} {
		res := performAdminRequest(t, app, http.MethodPut, "/api/admin/codex/default-model", body)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status %d: %s", res.Code, res.Body.String())
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) || app.service.CodexRemoteDefault().Model != "" {
			t.Fatal("failed save modified file/runtime")
		}
	}
}

func TestCatalogUnavailableIsUnverifiedNotUnsupported(t *testing.T) {
	app, _ := newFeishuAdminTestApp(t, config.DefaultAppConfig(), defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "old", "high")
	app.service.Instance("models-native").ModelCatalog.ErrorMessage = "network unavailable"
	res := performAdminRequest(t, app, http.MethodPut, "/api/admin/codex/default-model", `{"model":"gpt-6.1-sol","reasoningEffort":"high"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"validation":"unverified"`) {
		t.Fatalf("unavailable catalog: %d %s", res.Code, res.Body.String())
	}
}

func TestModelCatalogExcludesOfflineAndAPIProfiles(t *testing.T) {
	app, _ := newFeishuAdminTestApp(t, config.DefaultAppConfig(), defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "gpt-6.1-sol", "high")
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "api", Online: true, Backend: agentproto.BackendCodex, CodexProfileID: "cp_other", ModelCatalog: &agentproto.ModelCatalogSnapshot{Entries: []agentproto.ModelCatalogEntry{{Model: "private-model"}}}})
	res := performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/model-catalog", "")
	if !strings.Contains(res.Body.String(), "gpt-6.1-sol") || strings.Contains(res.Body.String(), "private-model") {
		t.Fatal(res.Body.String())
	}
	app.service.Instance("models-native").Online = false
	res = performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/model-catalog", "")
	if strings.Contains(res.Body.String(), "gpt-6.1-sol") || !strings.Contains(res.Body.String(), "unverified") {
		t.Fatal(res.Body.String())
	}
}

func TestDefaultSaveFailureAndUnsupportedPresetKeepRuntime(t *testing.T) {
	cfg := config.DefaultAppConfig()
	cfg.Codex.DefaultModel = "old-model"
	cfg.Codex.DefaultReasoningEffort = "high"
	app, path := newFeishuAdminTestApp(t, cfg, defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "new-model", "high")
	cfg.Codex.DefaultModel = "new-model"
	cfg.Codex.ModelPresets = map[string]config.CodexModelPreset{"sol": {Model: "unsupported", ReasoningEffort: "high"}}
	if err := config.WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	res := performAdminRequest(t, app, http.MethodPost, "/api/admin/codex/model-settings/apply", `{}`)
	if res.Code != http.StatusBadRequest || app.service.CodexRemoteDefault().Model != "old-model" {
		t.Fatalf("partially applied: %s", res.Body.String())
	}
	blocked := t.TempDir()
	app.admin.loadConfig = func() (config.LoadedAppConfig, error) {
		return config.LoadedAppConfig{Path: blocked, Config: config.DefaultAppConfig()}, nil
	}
	res = performAdminRequest(t, app, http.MethodPut, "/api/admin/codex/default-model", `{"model":"new-model","reasoningEffort":"high"}`)
	if res.Code != http.StatusInternalServerError || app.service.CodexRemoteDefault().Model != "old-model" {
		t.Fatalf("save failure changed runtime: %s", res.Body.String())
	}
}

func TestUnrelatedVSCodeCatalogDoesNotRejectRemoteDefault(t *testing.T) {
	app, _ := newFeishuAdminTestApp(t, config.DefaultAppConfig(), defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "new-model", "high")
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "vscode", Online: true, Source: "vscode", Backend: agentproto.BackendCodex, CodexProfileID: state.NativeCodexProfileID, ModelCatalog: &agentproto.ModelCatalogSnapshot{Entries: []agentproto.ModelCatalogEntry{{Model: "old-model"}}}})
	res := performAdminRequest(t, app, http.MethodPut, "/api/admin/codex/default-model", `{"model":"new-model","reasoningEffort":"high"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("unrelated catalog rejected default: %s", res.Body.String())
	}
}

func TestCatalogSuggestionsMatchValidationAcrossInstances(t *testing.T) {
	app, _ := newFeishuAdminTestApp(t, config.DefaultAppConfig(), defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")
	addModelSettingsCatalog(app, "new-model", "high", "medium")
	second := *app.service.Instance("models-native")
	second.InstanceID = "second"
	second.ModelCatalog = &agentproto.ModelCatalogSnapshot{Entries: []agentproto.ModelCatalogEntry{{Model: "new-model", SupportedReasoningEfforts: []agentproto.ReasoningEffortOption{{ReasoningEffort: "medium"}}}}}
	app.service.UpsertInstance(&second)
	res := performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/model-catalog", "")
	var catalog codexModelCatalogResponse
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries) != 1 || len(catalog.Entries[0].SupportedReasoningEfforts) != 1 || catalog.Entries[0].SupportedReasoningEfforts[0].ReasoningEffort != "medium" {
		t.Fatal(res.Body.String())
	}
	second.ModelCatalog.Entries[0].SupportedReasoningEfforts = []agentproto.ReasoningEffortOption{{ReasoningEffort: "low"}}
	res = performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/model-catalog", "")
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries) != 0 {
		t.Fatal(res.Body.String())
	}
	second.ModelCatalog.Entries[0].SupportedReasoningEfforts = nil
	res = performAdminRequest(t, app, http.MethodGet, "/api/admin/codex/model-catalog", "")
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Validation != "unverified" || len(catalog.Entries) != 1 || len(catalog.Entries[0].SupportedReasoningEfforts) != 2 {
		t.Fatal(res.Body.String())
	}
}
