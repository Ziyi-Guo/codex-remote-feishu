package modelconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

const applyPath = "/api/admin/codex/model-settings/apply"

type applyResponse struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	Validation      string `json:"validation"`
	Message         string `json:"message"`
	ConfigPath      string `json:"configPath"`
	Applied         bool   `json:"applied"`
	Error           struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func applyURL(override string, admin config.AdminSettings) (string, error) {
	target := strings.TrimSpace(override)
	if target == "" {
		host := strings.Trim(strings.TrimSpace(admin.ListenHost), "[]")
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		target = "http://" + net.JoinHostPort(host, strconv.Itoa(admin.ListenPort))
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "", errors.New("invalid admin URL; expected an HTTP loopback URL")
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("admin URL must be an HTTP loopback origin without credentials, path, query or fragment")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1" // Avoid DNS resolution of localhost and proxy interception.
	} else if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", errors.New("admin URL must use localhost or a loopback IP address")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("invalid admin URL port")
	}
	parsed.Host = net.JoinHostPort(host, port)
	parsed.Path = applyPath
	return parsed.String(), nil
}

func apply(ctx context.Context, endpoint, path string) (applyResponse, error) {
	var result applyResponse
	payload, err := json.Marshal(struct {
		ConfigPath string `json:"configPath"`
	}{path})
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("contact local daemon (check it is running and --admin-url): %w", err)
	}
	defer response.Body.Close()
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if decodeErr == nil && result.Error.Message != "" {
			return result, fmt.Errorf("model settings not applied (HTTP %d, %s): %s", response.StatusCode, result.Error.Code, result.Error.Message)
		}
		return result, fmt.Errorf("model settings not applied (HTTP %d)", response.StatusCode)
	}
	if decodeErr != nil {
		return result, errors.New("daemon returned an invalid model settings response")
	}
	if !result.Applied || (result.Validation != "verified" && result.Validation != "unverified") || result.ConfigPath == "" {
		return result, errors.New("daemon did not confirm model settings were applied")
	}
	return result, nil
}
