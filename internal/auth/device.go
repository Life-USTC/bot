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
	"github.com/Life-USTC/Bot/internal/store"
)

const oauthScope = "openid profile email offline_access"
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type Manager struct {
	Server     string
	HTTPClient *http.Client
	Store      *store.Store
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
	resp, err := m.httpClient().PostForm(meta.DeviceAuthorizationEndpoint, url.Values{
		"client_id": {clientID},
		"scope":     {oauthScope},
		"resource":  {m.resource(meta)},
	})
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device authorization failed (%d): %s", resp.StatusCode, string(body))
	}
	var out deviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
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
		ExpiresAt:               time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		IntervalSeconds:         interval,
		Status:                  "pending",
	}
	if err := m.Store.SaveLoginSession(ctx, ident, *session); err != nil {
		return nil, err
	}
	return session, nil
}

func (m *Manager) PollDeviceLogin(ctx context.Context, ident store.Identity) (PollResult, error) {
	session, err := m.Store.ActiveLoginSession(ctx, ident)
	if err != nil {
		return PollResult{}, err
	}
	if session == nil {
		return PollResult{Message: "暂无进行中的登录。发送：登录"}, nil
	}
	if time.Now().After(session.ExpiresAt) {
		_ = m.Store.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	}
	meta, err := m.discover(ctx)
	if err != nil {
		return PollResult{}, err
	}
	resp, err := m.httpClient().PostForm(meta.TokenEndpoint, url.Values{
		"grant_type":  {deviceGrantType},
		"client_id":   {session.ClientID},
		"device_code": {session.DeviceCode},
		"resource":    {m.resource(meta)},
	})
	if err != nil {
		return PollResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		cred, err := credentialFromTokenBody(session.ClientID, m.resource(meta), body, "", "")
		if err != nil {
			return PollResult{}, err
		}
		if err := m.Store.SaveCredential(ctx, ident, cred); err != nil {
			return PollResult{}, err
		}
		_ = m.Store.MarkLoginSession(ctx, ident, session.DeviceCode, "approved")
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
		_ = m.Store.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	case "access_denied":
		_ = m.Store.MarkLoginSession(ctx, ident, session.DeviceCode, "denied")
		return PollResult{Message: "登录已取消。发送：登录"}, nil
	default:
		return PollResult{}, fmt.Errorf("token poll failed (%d): %s", resp.StatusCode, string(body))
	}
}

func (m *Manager) AccessToken(ctx context.Context, ident store.Identity) (string, error) {
	cred, err := m.Store.Credential(ctx, ident)
	if err != nil {
		return "", err
	}
	if cred == nil {
		return "", ErrNotLoggedIn
	}
	if time.Until(cred.ExpiresAt) > time.Minute {
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	refreshed, err := m.refresh(ctx, *cred)
	if err != nil {
		return "", err
	}
	if err := m.Store.SaveCredential(ctx, ident, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (m *Manager) Logout(ctx context.Context, ident store.Identity) error {
	return m.Store.DeleteCredential(ctx, ident)
}

var ErrNotLoggedIn = fmt.Errorf("not logged in")
var ErrUnauthorized = fmt.Errorf("unauthorized")

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
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return store.Credential{}, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(body))
	}
	return credentialFromTokenBody(cred.ClientID, m.resource(meta), body, cred.RefreshToken, cred.Scope)
}

func (m *Manager) Refresh(ctx context.Context, ident store.Identity) (string, error) {
	cred, err := m.Store.Credential(ctx, ident)
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
	if err := m.Store.SaveCredential(ctx, ident, refreshed); err != nil {
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

func (m *Manager) discover(ctx context.Context) (metadata, error) {
	var lastErr error
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.Server, "/")+path, nil)
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
	data, _ := json.Marshal(body)
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
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("client registration failed (%d): %s", resp.StatusCode, string(body))
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

func (m *Manager) resource(meta metadata) string {
	if meta.Issuer != "" {
		return strings.TrimRight(meta.Issuer, "/")
	}
	return strings.TrimRight(m.Server, "/")
}

func credentialFromTokenBody(clientID, resource string, body []byte, fallbackRefresh, fallbackScope string) (store.Credential, error) {
	var tokens map[string]any
	if err := json.Unmarshal(body, &tokens); err != nil {
		return store.Credential{}, err
	}
	accessToken, _ := tokens["access_token"].(string)
	if accessToken == "" {
		return store.Credential{}, fmt.Errorf("token response missing access_token")
	}
	expiresIn := 3600.0
	if value, ok := tokens["expires_in"].(float64); ok {
		expiresIn = value
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		refreshToken = fallbackRefresh
	}
	scope, _ := tokens["scope"].(string)
	if scope == "" {
		scope = fallbackScope
	}
	tokenType, _ := tokens["token_type"].(string)
	return store.Credential{
		ClientID:     clientID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    tokenType,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scope:        scope,
		Resource:     resource,
	}, nil
}

func (m *Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}
