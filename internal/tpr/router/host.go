package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	codexUserAgent  = "codex_cli_rs/0.164.0"
	claudeUserAgent = "claude-cli/2.1.280 (external, cli)"
	claudeOAuthBeta = "oauth-2025-04-20"
	// Plans change rarely, so the profile endpoint is read at most this often.
	claudeProfileEvery = 24 * time.Hour
	maxUsageBody       = 1 << 20
)

// Endpoints are variables so end-to-end tests can point them at a mock upstream.
// They are the same read-only endpoints the panel uses.
var (
	CodexUsageURL    = "https://chatgpt.com/backend-api/wham/usage"
	ClaudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	ClaudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
)

// ModelResponse is the part of a model execution a window start needs.
type ModelResponse struct {
	StatusCode int
	Body       []byte
}

// Host is everything the router needs from the server. It replaces the plugin's
// host.auth.list, host.auth.get, host.http.do and host.model.execute callbacks
// with in-process calls, and tests fake it.
type Host interface {
	// Accounts lists the credentials backed by an auth file or runtime login.
	// It carries no secrets.
	Accounts() []engine.HostAccount
	// FetchQuota reads one account's quota from the provider's usage endpoint.
	FetchQuota(ctx context.Context, provider, id string) (engine.Quota, error)
	// ExecuteModel runs one non-streaming request locked to one account.
	ExecuteModel(ctx context.Context, provider, model string, body []byte, headers http.Header, authID string) (ModelResponse, error)
}

// managerHost implements Host on the auth manager. Tokens stay inside the
// executors: the manager's HttpRequest adds credentials to each call, so this
// code never reads one.
type managerHost struct {
	mgr *coreauth.Manager
	cfg func() *config.Config

	mu           sync.Mutex
	profileAfter map[string]time.Time
}

func newManagerHost(mgr *coreauth.Manager, cfg func() *config.Config) *managerHost {
	return &managerHost{mgr: mgr, cfg: cfg, profileAfter: map[string]time.Time{}}
}

func (h *managerHost) Accounts() []engine.HostAccount {
	auths := h.mgr.List()
	out := make([]engine.HostAccount, 0, len(auths))
	for _, a := range auths {
		if entry, ok := hostAccountFromAuth(a); ok {
			out = append(out, entry)
		}
	}
	return out
}

// hostAccountFromAuth mirrors the plugin host's auth listing: credentials from
// auth files and runtime logins are the roster, while config API keys never
// appear in it and count as present only while they are being offered.
func hostAccountFromAuth(a *coreauth.Auth) (engine.HostAccount, bool) {
	if a == nil {
		return engine.HostAccount{}, false
	}
	path := ""
	runtimeOnly := false
	if a.Attributes != nil {
		path = strings.TrimSpace(a.Attributes[coreauth.AttributePath])
		runtimeOnly = strings.EqualFold(strings.TrimSpace(a.Attributes["runtime_only"]), "true")
	}
	disabled := a.Disabled || a.Status == coreauth.StatusDisabled
	if path == "" && !runtimeOnly {
		return engine.HostAccount{}, false
	}
	if runtimeOnly && disabled {
		return engine.HostAccount{}, false
	}
	if path != "" && !runtimeOnly {
		// A deleted auth file lingers in memory, disabled, until it is cleaned up.
		if _, err := os.Stat(path); os.IsNotExist(err) && (disabled || strings.EqualFold(strings.TrimSpace(a.StatusMessage), "removed via management api")) {
			return engine.HostAccount{}, false
		}
	}
	a.EnsureIndex()
	return engine.HostAccount{
		ID:          a.ID,
		AuthIndex:   a.Index,
		Provider:    strings.TrimSpace(a.Provider),
		Label:       a.Label,
		Email:       authEmail(a),
		Plan:        authPlan(a),
		Disabled:    a.Disabled,
		Unavailable: a.Unavailable,
		Status:      string(a.Status),
		NextRetry:   a.NextRetryAfter,
	}, true
}

func authEmail(a *coreauth.Auth) string {
	if a.Metadata != nil {
		if v, ok := a.Metadata["email"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"email", "account_email"} {
			if v := strings.TrimSpace(a.Attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}

func authPlan(a *coreauth.Auth) string {
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["plan_type"]); v != "" {
			return v
		}
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["plan_type"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (h *managerHost) FetchQuota(ctx context.Context, provider, id string) (engine.Quota, error) {
	auth, ok := h.mgr.GetByID(id)
	if !ok || auth == nil {
		return engine.Quota{}, errors.New("account is no longer in the proxy")
	}
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	switch provider {
	case engine.ProviderCodex:
		return h.fetchCodex(ctx, auth)
	case engine.ProviderClaude:
		return h.fetchClaude(ctx, auth)
	}
	return engine.Quota{}, fmt.Errorf("no quota endpoint for provider %q", provider)
}

func (h *managerHost) fetchCodex(ctx context.Context, auth *coreauth.Auth) (engine.Quota, error) {
	header := http.Header{"Accept": {"application/json"}, "User-Agent": {codexUserAgent}}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["account_id"].(string); ok && strings.TrimSpace(v) != "" {
			header.Set("Chatgpt-Account-Id", strings.TrimSpace(v))
		}
	}
	body, err := h.get(ctx, auth, CodexUsageURL, header)
	if err != nil {
		return engine.Quota{}, err
	}
	q, ok := engine.ParseUsageResponse(body, time.Now())
	if !ok {
		return engine.Quota{}, errors.New("usage endpoint response had no rate-limit windows")
	}
	return q, nil
}

func (h *managerHost) fetchClaude(ctx context.Context, auth *coreauth.Auth) (engine.Quota, error) {
	header := http.Header{
		"Accept":         {"application/json"},
		"Content-Type":   {"application/json"},
		"User-Agent":     {claudeUserAgent},
		"Anthropic-Beta": {claudeOAuthBeta},
	}
	body, err := h.get(ctx, auth, ClaudeUsageURL, header)
	if err != nil {
		return engine.Quota{}, err
	}
	q, ok := engine.ParseClaudeUsageResponse(body, time.Now())
	if !ok {
		return engine.Quota{}, errors.New("usage endpoint response had no rate-limit windows")
	}
	if h.profileDue(auth.ID) {
		// The plan only sizes the account in the pool's totals, so a failure here
		// is not a failed quota check.
		if raw, errProfile := h.get(ctx, auth, ClaudeProfileURL, header); errProfile == nil {
			q.Plan = engine.ParseClaudeProfilePlan(raw)
		}
	}
	return q, nil
}

func (h *managerHost) profileDue(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if now.Before(h.profileAfter[id]) {
		return false
	}
	h.profileAfter[id] = now.Add(claudeProfileEvery)
	return true
}

// get calls a usage endpoint through the account's executor, which adds the
// credential and honours its proxy settings. Errors name only the status: a
// provider's error body is not worth the risk of echoing something sensitive.
func (h *managerHost) get(ctx context.Context, auth *coreauth.Auth, url string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("could not build the usage request")
	}
	req.Header = header
	resp, err := h.mgr.HttpRequest(ctx, auth, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("usage request timed out after %s", pollTimeout)
		}
		return nil, errors.New("usage request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUsageBody))
		return nil, fmt.Errorf("usage endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBody))
	if err != nil {
		return nil, errors.New("could not read the usage response")
	}
	return body, nil
}

// ExecuteModel sends one request through the proxy's normal pipeline, locked to
// one account: routing, translation, payload rules and usage accounting all apply.
func (h *managerHost) ExecuteModel(ctx context.Context, provider, model string, body []byte, headers http.Header, authID string) (ModelResponse, error) {
	var sdkCfg *config.SDKConfig
	if cfg := h.cfg(); cfg != nil {
		sdkCfg = &cfg.SDKConfig
	}
	protocol := "openai-response"
	if provider == engine.ProviderClaude {
		protocol = "claude"
	}
	resp, errMsg := handlers.NewBaseAPIHandlers(sdkCfg, h.mgr).ExecuteModel(ctx, handlers.ModelExecutionRequest{
		EntryProtocol:  protocol,
		ExitProtocol:   protocol,
		Model:          model,
		Body:           body,
		Headers:        headers,
		ForcedProvider: provider,
		AuthID:         authID,
	})
	if errMsg != nil {
		if errMsg.Error != nil {
			return ModelResponse{StatusCode: errMsg.StatusCode}, errMsg.Error
		}
		return ModelResponse{StatusCode: errMsg.StatusCode}, errors.New("model execution failed")
	}
	return ModelResponse{StatusCode: resp.StatusCode, Body: resp.Body}, nil
}

// starterBody builds the single tiny request that starts a 5-hour window.
func starterBody(provider, model, effort, oneOff string) ([]byte, http.Header) {
	if provider == engine.ProviderClaude {
		payload := map[string]any{
			"model":      model,
			"max_tokens": 16,
			"messages":   []any{map[string]any{"role": "user", "content": "Reply with the single word OK."}},
			"metadata":   map[string]any{"user_id": oneOff},
			"stream":     false,
		}
		body, _ := json.Marshal(payload)
		return body, http.Header{"X-Claude-Code-Session-Id": {oneOff}}
	}
	body, _ := json.Marshal(map[string]any{
		"prompt_cache_key": oneOff,
		"model":            model,
		"instructions":     "Reply with the single word OK.",
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "OK"}},
		}},
		"reasoning": map[string]any{"effort": effort},
		"store":     false,
		"stream":    false,
	})
	return body, http.Header{"Session_id": {oneOff}}
}
