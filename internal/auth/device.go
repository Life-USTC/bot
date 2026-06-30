package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
	"golang.org/x/oauth2"
)

const oauthScope = "openid profile email offline_access rest:read rest:write"

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
		return nil, errors.New("server does not support OAuth device authorization")
	}
	if meta.RegistrationEndpoint == "" {
		return nil, errors.New("server does not advertise OAuth dynamic client registration")
	}
	clientID, err := m.registerClient(ctx, meta.RegistrationEndpoint)
	if err != nil {
		return nil, err
	}

	conf := m.oauth2Config(meta, clientID)
	res, err := conf.DeviceAuth(m.oidcContext(ctx), oauth2.SetAuthURLParam("resource", m.resource(meta)))
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", sanitizeDeviceAuthError(err))
	}
	if err := validateDeviceAuthResponse(res); err != nil {
		return nil, err
	}

	interval := int(res.Interval)
	if interval <= 0 {
		interval = 5
	}
	expiresIn := 600
	if !res.Expiry.IsZero() {
		expiresIn = int(time.Until(res.Expiry).Seconds() + 0.5)
		if expiresIn <= 0 {
			expiresIn = 600
		}
	}

	session := &store.LoginSession{
		DeviceCode:              res.DeviceCode,
		UserCode:                res.UserCode,
		VerificationURI:         res.VerificationURI,
		VerificationURIComplete: res.VerificationURIComplete,
		ClientID:                clientID,
		ExpiresAt:               m.now().Add(time.Duration(expiresIn) * time.Second),
		IntervalSeconds:         interval,
		Status:                  "pending",
	}
	if err := authStore.SaveLoginSession(ctx, ident, *session); err != nil {
		return nil, err
	}
	return session, nil
}

func sanitizeDeviceAuthError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		body := textutil.TrimBytesRunes(bytes.TrimSpace(re.Body), 200)
		if re.ErrorCode != "" {
			return fmt.Errorf("oauth2: %q %q: %s", re.ErrorCode, re.ErrorDescription, body)
		}
		status := "unknown"
		if re.Response != nil {
			status = re.Response.Status
		}
		return fmt.Errorf("oauth2: cannot fetch token: %s: %s", status, body)
	}
	return err
}

func validateDeviceAuthResponse(resp *oauth2.DeviceAuthResponse) error {
	switch {
	case resp == nil:
		return errors.New("device authorization response missing")
	case resp.DeviceCode == "":
		return errors.New("device authorization response missing device_code")
	case resp.UserCode == "":
		return errors.New("device authorization response missing user_code")
	case resp.VerificationURI == "" && resp.VerificationURIComplete == "":
		return errors.New("device authorization response missing verification_uri")
	case resp.Expiry.IsZero():
		return errors.New("device authorization response missing expires_in")
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

	conf := m.oauth2Config(meta, session.ClientID)
	// DeviceAccessToken uses real time for its internal deadline; convert the
	// manager-clock-relative expiration to a real future time.
	remaining := session.ExpiresAt.Sub(m.now())
	if remaining <= 0 {
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	}
	dar := &oauth2.DeviceAuthResponse{
		DeviceCode:              session.DeviceCode,
		UserCode:                session.UserCode,
		VerificationURI:         session.VerificationURI,
		VerificationURIComplete: session.VerificationURIComplete,
		Expiry:                  time.Now().Add(remaining),
		Interval:                int64(session.IntervalSeconds),
	}

	pollTimeout := time.Duration(session.IntervalSeconds)*time.Second + 2*time.Second
	if pollTimeout <= 0 {
		pollTimeout = 7 * time.Second
	}
	pollCtx, cancel := context.WithTimeout(m.oidcContext(ctx), pollTimeout)
	defer cancel()

	tok, err := conf.DeviceAccessToken(pollCtx, dar, oauth2.SetAuthURLParam("resource", m.resource(meta)))
	if err != nil {
		return m.mapPollError(ctx, ident, *session, err)
	}

	issuer := m.expectedIssuer(meta)
	audience := session.ClientID
	vt := newVerifiedToken(tok)
	if err := vt.ValidateIDToken(issuer, audience, m.now()); err != nil {
		return PollResult{}, err
	}

	cred, err := verifiedTokenToCredential(session.ClientID, m.resource(meta), vt, "", "", m.now())
	if err != nil {
		return PollResult{}, err
	}
	if err := authStore.SaveCredential(ctx, ident, cred); err != nil {
		return PollResult{}, err
	}
	_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "approved")
	return PollResult{Authorized: true, Message: "登录完成。"}, nil
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

var ErrNotLoggedIn = errors.New("not logged in")
var ErrStoreNotConfigured = errors.New("auth store not configured")

func (m *Manager) refresh(ctx context.Context, cred store.Credential) (store.Credential, error) {
	meta, err := m.discover(ctx)
	if err != nil {
		return store.Credential{}, err
	}
	conf := m.oauth2Config(meta, cred.ClientID)
	token := &oauth2.Token{
		RefreshToken: cred.RefreshToken,
		Expiry:       m.now().Add(-time.Hour),
	}

	ctx = m.oidcContext(ctx)
	// NOTE: golang.org/x/oauth2.TokenSource does not support extra parameters on
	// refresh. The server's resource validation only runs when a resource is
	// explicitly supplied, so omitting it is safe for this provider. If the
	// server later requires a resource indicator on refresh, implement a custom
	// token source that includes it in the refresh_token grant request.
	tok, err := conf.TokenSource(ctx, token).Token()
	if err != nil {
		return store.Credential{}, m.mapRefreshError(err)
	}

	issuer := m.expectedIssuer(meta)
	audience := cred.ClientID
	vt := newVerifiedToken(tok)
	if err := vt.ValidateIDToken(issuer, audience, m.now()); err != nil {
		return store.Credential{}, err
	}
	return verifiedTokenToCredential(cred.ClientID, m.resource(meta), vt, cred.RefreshToken, cred.Scope, m.now())
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
		"grant_types":                []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
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
		return "", errors.New("client registration response missing client_id")
	}
	return result.ClientID, nil
}

func (m *Manager) oauth2Config(meta metadata, clientID string) oauth2.Config {
	return oauth2.Config{
		ClientID: clientID,
		Scopes:   strings.Fields(oauthScope),
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: meta.DeviceAuthorizationEndpoint,
			TokenURL:      meta.TokenEndpoint,
		},
	}
}

type contextBoundTransport struct {
	base http.RoundTripper
	ctx  context.Context
}

func (t *contextBoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req.WithContext(t.ctx))
}

func contextBoundHTTPClient(ctx context.Context, client *http.Client) *http.Client {
	return &http.Client{
		Timeout:   client.Timeout,
		Transport: &contextBoundTransport{base: client.Transport, ctx: ctx},
	}
}

func (m *Manager) oidcContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, contextBoundHTTPClient(ctx, m.httpClient()))
}

func (m *Manager) expectedIssuer(meta metadata) string {
	if iss := strings.TrimSpace(meta.Issuer); iss != "" {
		return strings.TrimRight(iss, "/")
	}
	return m.serverURL()
}

func (m *Manager) mapPollError(ctx context.Context, ident store.Identity, session store.LoginSession, err error) (PollResult, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return PollResult{Pending: true, Message: "等待确认登录。"}, nil
	}

	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		switch re.ErrorCode {
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
		}

		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		if status == http.StatusBadRequest && !json.Valid(re.Body) {
			return PollResult{}, fmt.Errorf("token poll returned invalid JSON (%d): %w", status, err)
		}
	}

	if msg := err.Error(); strings.Contains(msg, "read failed") {
		return PollResult{}, fmt.Errorf("token poll response read failed: %w", err)
	}

	return PollResult{}, fmt.Errorf("token poll failed: %w", err)
}

func (m *Manager) mapRefreshError(err error) error {
	if msg := err.Error(); strings.Contains(msg, "read failed") {
		return fmt.Errorf("refresh response read failed: %w", err)
	}
	return err
}

func (m *Manager) resource(meta metadata) string {
	if issuer := strings.TrimSpace(meta.Issuer); issuer != "" {
		return strings.TrimRight(issuer, "/")
	}
	return m.serverURL()
}

func (m *Manager) serverURL() string {
	return textutil.TrimTrailingSlash(m.Server)
}

func responseBodyText(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read response body: " + err.Error()
	}
	return bodyText(body)
}

func bodyText(body []byte) string {
	return textutil.TrimBytesRunes(body, 200)
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
