package modelconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

func testConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckIsOfflineAndSanitized(t *testing.T) {
	path := testConfig(t, `{"codex":{"defaultModel":"gpt-test","defaultReasoningEffort":"high","modelPresets":{"mine":{"model":"gpt-other","reasoningEffort":"low"}}},"feishu":{"apps":[{"id":"test","appId":"cli_test","appSecret":"secret-never-print"}]}}`)
	var out, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"check", "--config", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Config valid:", "gpt-test", "Preset [mine]", "gpt-other", "unverified (offline check)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q missing %q", out.String(), want)
		}
	}
	if strings.Contains(out.String()+stderr.String(), "secret-never-print") {
		t.Fatal("leaked secret")
	}
}

func TestCheckRejectsBadInputs(t *testing.T) {
	malformed := testConfig(t, `{"codex":`)
	invalid := testConfig(t, `{"codex":{"defaultModel":"gpt-test"}}`)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing", []string{"check", "--config", filepath.Join(t.TempDir(), "missing.json")}, "must exist"},
		{"malformed", []string{"check", "--config", malformed}, "invalid config"},
		{"unpaired", []string{"check", "--config", invalid}, "invalid config"},
		{"directory", []string{"check", "--config", t.TempDir()}, "regular file"},
		{"action", []string{"watch"}, "expected check or apply"},
		{"extra", []string{"check", "surprise"}, "unexpected positional"},
		{"check URL", []string{"check", "--admin-url", "http://127.0.0.1:1"}, "only supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(context.Background(), tc.args, &out, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplySendsCanonicalPathAndPrintsValidation(t *testing.T) {
	path := testConfig(t, `{"codex":{"defaultModel":"gpt-test","defaultReasoningEffort":"high"}}`)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skip(err)
	}
	for _, validation := range []string{"verified", "unverified"} {
		t.Run(validation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != applyPath {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["configPath"] != canonical || len(body) != 1 {
					t.Errorf("body = %#v", body)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "reasoningEffort": "high", "validation": validation, "applied": true, "configPath": canonical})
			}))
			defer server.Close()
			t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
			t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
			var out bytes.Buffer
			if err := Run(context.Background(), []string{"apply", "--config", link, "--admin-url", server.URL}, &out, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Model settings applied:") || !strings.Contains(out.String(), validation) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestApplyErrorsAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"rejected", http.StatusBadRequest, `{"error":{"code":"invalid_model","message":"model is unsupported"}}`, "model is unsupported"},
		{"secret HTML", http.StatusInternalServerError, "secret-never-print", "HTTP 500"},
		{"bad response", http.StatusOK, "secret-never-print", "invalid model settings response"},
		{"unconfirmed", http.StatusOK, `{"applied":false}`, "did not confirm"},
		{"redirect", http.StatusTemporaryRedirect, "", "HTTP 307"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://192.0.2.1/")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := apply(context.Background(), server.URL, "/config.json")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "secret-never-print") {
				t.Fatal("leaked response body")
			}
		})
	}
}

func TestApplyURLLocalOnly(t *testing.T) {
	for _, input := range []string{"https://127.0.0.1", "http://192.0.2.1", "http://example.com", "http://user:password@localhost", "http://localhost/path", "http://localhost?q=secret", "http://localhost#fragment", "http://localhost:0", "http://localhost:99999"} {
		if _, err := applyURL(input, config.AdminSettings{}); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, host := range []string{"0.0.0.0", "::", "localhost", "127.0.0.1"} {
		got, err := applyURL("", config.AdminSettings{ListenHost: host, ListenPort: 9501})
		if err != nil || got != "http://127.0.0.1:9501"+applyPath {
			t.Errorf("host %q: %q %v", host, got, err)
		}
	}
	got, err := applyURL("http://[::1]:9501", config.AdminSettings{})
	if err != nil || got != "http://[::1]:9501"+applyPath {
		t.Fatalf("IPv6: %q %v", got, err)
	}
}
