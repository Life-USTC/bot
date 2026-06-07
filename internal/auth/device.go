package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

const oauthScope = "openid profile email offline_access"
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type Manager struct {
	Server     string
	HTTPClient *http.Client
	Store      *store.Store
	Now        func() time.Time
}

type metadata struct {
	Issuer                      string `json:"issuer"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	RegistrationEndpoint        string `json:"registration_endpoint"`
}

type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type PollResult struct {
	Pending    bool
	SlowDown   bool
	Authorized bool
	Message    string
}

func (m *Manager) BeginDeviceLogin(ctx context.Context, ident store.Identity) (*store.LoginSession, error) {
	authStore, err := m.requireStore()
	if err != nil {
		return nil, err
	}
	meta, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	if meta.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("server does not support OAuth device authorization")
	}
	if meta.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("server does not advertise OAuth dynamic client registration")
	}
	clientID, err := m.registerClient(ctx, meta.RegistrationEndpoint)
	if err != nil {
		return nil, err
	}
	resp, err := m.postForm(ctx, meta.DeviceAuthorizationEndpoint, url.Values{
		"client_id": {clientID},
		"scope":     {oauthScope},
		"resource":  {m.resource(meta)},
	})
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed (%d): %s", resp.StatusCode, responseBodyText(resp))
	}
	var out deviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if err := validateDeviceAuthResponse(out); err != nil {
		return nil, err
	}
	interval := out.Interval
	if interval <= 0 {
		interval = 5
	}
	session := &store.LoginSession{
		DeviceCode:              out.DeviceCode,
		UserCode:                out.UserCode,
		VerificationURI:         out.VerificationURI,
		VerificationURIComplete: out.VerificationURIComplete,
		ClientID:                clientID,
		ExpiresAt:               m.now().Add(time.Duration(out.ExpiresIn) * time.Second),
		IntervalSeconds:         interval,
		Status:                  "pending",
	}
	if err := authStore.SaveLoginSession(ctx, ident, *session); err != nil {
		return nil, err
	}
	return session, nil
}

func validateDeviceAuthResponse(resp deviceAuthResponse) error {
	switch {
	case resp.DeviceCode == "":
		return fmt.Errorf("device authorization response missing device_code")
	case resp.UserCode == "":
		return fmt.Errorf("device authorization response missing user_code")
	case resp.VerificationURI == "" && resp.VerificationURIComplete == "":
		return fmt.Errorf("device authorization response missing verification_uri")
	case resp.ExpiresIn <= 0:
		return fmt.Errorf("device authorization response missing expires_in")
	default:
		return nil
	}
}

func (m *Manager) PollDeviceLogin(ctx context.Context, ident store.Identity) (PollResult, error) {
	authStore, err := m.requireStore()
	if err != nil {
		return PollResult{}, err
	}
	session, err := authStore.ActiveLoginSession(ctx, ident)
	if err != nil {
		return PollResult{}, err
	}
	if session == nil {
		return PollResult{Message: "暂无进行中的登录。发送：登录"}, nil
	}
	if m.now().After(session.ExpiresAt) {
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	}
	meta, err := m.discover(ctx)
	if err != nil {
		return PollResult{}, err
	}
	resp, err := m.postForm(ctx, meta.TokenEndpoint, url.Values{
		"grant_type":  {deviceGrantType},
		"client_id":   {session.ClientID},
		"device_code": {session.DeviceCode},
		"resource":    {m.resource(meta)},
	})
	if err != nil {
		return PollResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return PollResult{}, fmt.Errorf("token poll response read failed: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		cred, err := credentialFromTokenBodyAt(session.ClientID, m.resource(meta), body, "", "", m.now())
		if err != nil {
			return PollResult{}, err
		}
		if err := authStore.SaveCredential(ctx, ident, cred); err != nil {
			return PollResult{}, err
		}
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "approved")
		return PollResult{Authorized: true, Message: "登录完成。"}, nil
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return PollResult{}, fmt.Errorf("token poll returned invalid JSON (%d): %w", resp.StatusCode, err)
	}
	switch errResp.Error {
	case "authorization_pending":
		return PollResult{Pending: true, Message: "等待确认登录。"}, nil
	case "slow_down":
		return PollResult{SlowDown: true, Message: "轮询频率受限，稍后继续检查。"}, nil
	case "expired_token":
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	case "access_denied":
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "denied")
		return PollResult{Message: "登录已取消。发送：登录"}, nil
	default:
		return PollResult{}, fmt.Errorf("token poll failed (%d): %s", resp.StatusCode, bodyText(body))
	}
}

func (m *Manager) AccessToken(ctx context.Context, ident store.Identity) (string, error) {
	authStore, err := m.requireStore()
	if err != nil {
		return "", err
	}
	cred, err := authStore.Credential(ctx, ident)
	if err != nil {
		return "", err
	}
	if cred == nil {
		return "", ErrNotLoggedIn
	}
	if cred.ExpiresAt.Sub(m.now()) > time.Minute {
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	refreshed, err := m.refresh(ctx, *cred)
	if err != nil {
		return "", err
	}
	if err := authStore.SaveCredential(ctx, ident, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (m *Manager) Logout(ctx context.Context, ident store.Identity) error {
	authStore, err := m.requireStore()
	if err != nil {
		return err
	}
	return authStore.DeleteCredential(ctx, ident)
}

var ErrNotLoggedIn = fmt.Errorf("not logged in")
var ErrUnauthorized = fmt.Errorf("unauthorized")
var ErrStoreNotConfigured = fmt.Errorf("auth store not configured")

func (m *Manager) refresh(ctx context.Context, cred store.Credential) (store.Credential, error) {
	meta, err := m.discover(ctx)
	if err != nil {
		return store.Credential{}, err
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cred.ClientID},
		"refresh_token": {cred.RefreshToken},
		"resource":      {m.resource(meta)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return store.Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return store.Credential{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return store.Credential{}, fmt.Errorf("refresh response read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return store.Credential{}, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, bodyText(body))
	}
	return credentialFromTokenBodyAt(cred.ClientID, m.resource(meta), body, cred.RefreshToken, cred.Scope, m.now())
}

func (m *Manager) Refresh(ctx context.Context, ident store.Identity) (string, error) {
	authStore, err := m.requireStore()
	if err != nil {
		return "", err
	}
	cred, err := authStore.Credential(ctx, ident)
	if err != nil {
		return "", err
	}
	if cred == nil || cred.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	refreshed, err := m.refresh(ctx, *cred)
	if err != nil {
		return "", err
	}
	if err := authStore.SaveCredential(ctx, ident, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (m *Manager) RefreshIfUnauthorized(ctx context.Context, ident store.Identity, err error) (string, bool) {
	if !life.IsUnauthorized(err) {
		return "", false
	}
	refreshed, refreshErr := m.Refresh(ctx, ident)
	return refreshed, refreshErr == nil
}

func (m *Manager) requireStore() (*store.Store, error) {
	if m == nil || m.Store == nil {
		return nil, ErrStoreNotConfigured
	}
	return m.Store, nil
}

func (m *Manager) discover(ctx context.Context) (metadata, error) {
	var lastErr error
	server := m.serverURL()
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+path, nil)
		if err != nil {
			return metadata{}, err
		}
		resp, err := m.httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			var out metadata
			err = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			return out, err
		}
		lastErr = fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, responseBodyText(resp))
		_ = resp.Body.Close()
	}
	return metadata{}, fmt.Errorf("could not discover OAuth metadata from %s: %v", m.Server, lastErr)
}

func (m *Manager) registerClient(ctx context.Context, endpoint string) (string, error) {
	body := map[string]any{
		"client_name":                "life-ustc-onebot",
		"redirect_uris":              []string{"http://localhost/callback"},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      oauthScope,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("client registration failed (%d): %s", resp.StatusCode, responseBodyText(resp))
	}
	var result struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.ClientID == "" {
		return "", fmt.Errorf("client registration response missing client_id")
	}
	return result.ClientID, nil
}

func (m *Manager) postForm(ctx context.Context, endpoint string, values url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return m.httpClient().Do(req)
}

func (m *Manager) resource(meta metadata) string {
	if issuer := strings.TrimSpace(meta.Issuer); issuer != "" {
		return strings.TrimRight(issuer, "/")
	}
	return m.serverURL()
}

func (m *Manager) serverURL() string {
	return strings.TrimRight(strings.TrimSpace(m.Server), "/")
}

func responseBodyText(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read response body: " + err.Error()
	}
	return bodyText(body)
}

func bodyText(body []byte) string {
	return strings.TrimSpace(string(body))
}

func credentialFromTokenBody(clientID, resource string, body []byte, fallbackRefresh, fallbackScope string) (store.Credential, error) {
	return credentialFromTokenBodyAt(clientID, resource, body, fallbackRefresh, fallbackScope, time.Now())
}

func credentialFromTokenBodyAt(clientID, resource string, body []byte, fallbackRefresh, fallbackScope string, now time.Time) (store.Credential, error) {
	var tokens map[string]any
	if err := json.Unmarshal(body, &tokens); err != nil {
		return store.Credential{}, err
	}
	accessToken := tokenString(tokens, "access_token", "")
	if accessToken == "" {
		return store.Credential{}, fmt.Errorf("token response missing access_token")
	}
	expiresIn := 3600
	if value, ok := lifedata.IntValue(tokens["expires_in"]); ok {
		expiresIn = value
	}
	refreshToken := tokenString(tokens, "refresh_token", fallbackRefresh)
	scope := tokenString(tokens, "scope", fallbackScope)
	tokenType := tokenString(tokens, "token_type", "")
	return store.Credential{
		ClientID:     clientID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    tokenType,
		ExpiresAt:    now.Add(time.Duration(expiresIn) * time.Second),
		Scope:        scope,
		Resource:     resource,
	}, nil
}

func tokenString(tokens map[string]any, key, fallback string) string {
	value, _ := tokens[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (m *Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
