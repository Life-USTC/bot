package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

var oauthScope = strings.Join([]string{
	"openid",
	"profile",
	"email",
	"offline_access",
	"account.profile:read",
	"workspace.todo:read",
	"workspace.todo:write",
	"workspace.homework:read",
	"workspace.homework:write",
	"workspace.subscription:read",
	"workspace.subscription:write",
	"community.comment:read",
	"community.comment:write",
	"community.description:read",
	"community.description:write",
	"workspace.upload:read",
	"workspace.upload:write",
	"workspace.overview:read",
	"workspace.link-pin:read",
	"workspace.link-pin:write",
	"catalog.bus:read",
	"workspace.bus-preferences:read",
	"workspace.bus-preferences:write",
	"catalog.course:read",
	"catalog.section:read",
	"catalog.teacher:read",
	"catalog.schedule:read",
	"workspace.schedule:read",
	"catalog.exam:read",
	"workspace.exam:read",
}, " ")

type Manager struct {
	Server     string
	HTTPClient *http.Client
	Store      *store.Store
	Now        func() time.Time
	refreshes  singleflight.Group

	metaMu    sync.Mutex
	metaCache *metadata
	metaAt    time.Time
}

const oidcMetadataTTL = 30 * time.Minute

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

	resources := m.resources(meta)
	res, err := m.deviceAuth(ctx, meta.DeviceAuthorizationEndpoint, clientID, resources)
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
		Resources:               joinResources(resources),
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

func splitResources(resource string) []string {
	parts := strings.Fields(resource)
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func joinResources(resources []string) string {
	return strings.Join(splitResources(strings.Join(resources, " ")), " ")
}

func normalizeResourceURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func resourceURLsMatch(left, right string) bool {
	return normalizeResourceURL(left) == normalizeResourceURL(right)
}

func tokenAudienceValues(accessToken string) []string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil
	}
	aud, ok := claims["aud"]
	if !ok {
		return nil
	}
	switch typed := aud.(type) {
	case string:
		if typed = strings.TrimSpace(typed); typed != "" {
			return []string{typed}
		}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

func tokenAudienceMatches(accessToken, resource string) bool {
	audiences := tokenAudienceValues(accessToken)
	if len(audiences) == 0 {
		return true
	}
	resource = normalizeResourceURL(resource)
	for _, audience := range audiences {
		if resourceURLsMatch(audience, resource) {
			return true
		}
	}
	return false
}

func approvedRefreshResource(approved []string, resource string) string {
	for _, candidate := range approved {
		if resourceURLsMatch(candidate, resource) {
			return normalizeResourceURL(candidate)
		}
	}
	return normalizeResourceURL(resource)
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

	// DeviceAccessToken uses real time for its internal deadline; convert the
	// manager-clock-relative expiration to a real future time.
	remaining := session.ExpiresAt.Sub(m.now())
	if remaining <= 0 {
		_ = authStore.MarkLoginSession(ctx, ident, session.DeviceCode, "expired")
		return PollResult{Message: "验证码已过期。发送：登录"}, nil
	}
	pollTimeout := time.Duration(session.IntervalSeconds)*time.Second + 2*time.Second
	if pollTimeout <= 0 {
		pollTimeout = 7 * time.Second
	}
	pollCtx, cancel := context.WithTimeout(m.oidcContext(ctx), pollTimeout)
	defer cancel()

	resources := splitResources(session.Resources)
	if len(resources) == 0 {
		resources = []string{m.resource(meta)}
	}
	tok, err := m.deviceAccessToken(pollCtx, meta.TokenEndpoint, session.ClientID, session.DeviceCode, resources)
	if err != nil {
		return m.mapPollError(ctx, ident, *session, err)
	}

	issuer := m.expectedIssuer(meta)
	audience := session.ClientID
	vt := newVerifiedToken(tok)
	if err := vt.ValidateIDToken(issuer, audience, m.now()); err != nil {
		return PollResult{}, err
	}

	cred, err := verifiedTokenToCredential(session.ClientID, joinResources(resources), vt, "", "", m.now())
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
	return m.accessTokenForResource(ctx, ident, "rest")
}

func (m *Manager) MCPAccessToken(ctx context.Context, ident store.Identity) (string, error) {
	return m.accessTokenForResource(ctx, ident, "mcp")
}

func (m *Manager) accessTokenForResource(
	ctx context.Context,
	ident store.Identity,
	purpose string,
) (string, error) {
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

	if cred.ExpiresAt.Sub(m.now()) > time.Minute &&
		len(tokenAudienceValues(cred.AccessToken)) == 0 {
		return cred.AccessToken, nil
	}

	meta, err := m.discover(ctx)
	if err != nil {
		return "", err
	}
	targetResource := m.resource(meta)
	if purpose == "mcp" {
		targetResource = m.mcpResource()
	}

	if cred.ExpiresAt.Sub(m.now()) > time.Minute &&
		tokenAudienceMatches(cred.AccessToken, targetResource) {
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	return m.refreshStoredCredential(ctx, ident, false, purpose)
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

func (m *Manager) refresh(ctx context.Context, cred store.Credential, resources []string) (store.Credential, error) {
	meta, err := m.discover(ctx)
	if err != nil {
		return store.Credential{}, err
	}
	if len(resources) == 0 {
		resources = []string{m.resource(meta)}
	}
	tok, err := m.refreshTokenRequest(ctx, meta.TokenEndpoint, cred.ClientID, cred.RefreshToken, cred.Scope, resources)
	if err != nil {
		return store.Credential{}, m.mapRefreshError(err)
	}

	issuer := m.expectedIssuer(meta)
	audience := cred.ClientID
	vt := newVerifiedToken(tok)
	if err := vt.ValidateIDToken(issuer, audience, m.now()); err != nil {
		return store.Credential{}, err
	}
	return verifiedTokenToCredential(
		cred.ClientID,
		cred.Resource,
		vt,
		cred.RefreshToken,
		cred.Scope,
		m.now(),
	)
}

func (m *Manager) refreshTokenRequest(ctx context.Context, endpoint, clientID, refreshToken, scope string, resources []string) (*oauth2.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	if scope = strings.TrimSpace(scope); scope != "" {
		form.Set("scope", scope)
	}
	for _, resource := range resources {
		if resource = strings.TrimSpace(resource); resource != "" {
			form.Add("resource", resource)
		}
	}

	return m.tokenRequest(ctx, endpoint, form)
}

func (m *Manager) deviceAuth(ctx context.Context, endpoint, clientID string, resources []string) (*oauth2.DeviceAuthResponse, error) {
	if endpoint == "" {
		return nil, errors.New("server does not support OAuth device authorization")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", oauthScope)
	for _, resource := range resources {
		if resource = strings.TrimSpace(resource); resource != "" {
			form.Add("resource", resource)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, retrieveError(resp, body)
	}
	var dar oauth2.DeviceAuthResponse
	if err := json.Unmarshal(body, &dar); err != nil {
		return nil, fmt.Errorf("device authorization returned %d with invalid JSON", resp.StatusCode)
	}
	return &dar, nil
}

func (m *Manager) deviceAccessToken(ctx context.Context, endpoint, clientID, deviceCode string, resources []string) (*oauth2.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	for _, resource := range resources {
		if resource = strings.TrimSpace(resource); resource != "" {
			form.Add("resource", resource)
		}
	}
	return m.tokenRequest(ctx, endpoint, form)
}

func (m *Manager) tokenRequest(ctx context.Context, endpoint string, form url.Values) (*oauth2.Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, retrieveError(resp, body)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("token response returned %d with invalid JSON", resp.StatusCode)
	}

	accessToken, _ := raw["access_token"].(string)
	tokenType, _ := raw["token_type"].(string)
	nextRefreshToken, _ := raw["refresh_token"].(string)
	expiresIn := tokenExpiresInFromRaw(raw)
	tok := &oauth2.Token{
		AccessToken:  strings.TrimSpace(accessToken),
		TokenType:    strings.TrimSpace(tokenType),
		RefreshToken: strings.TrimSpace(nextRefreshToken),
		ExpiresIn:    int64(expiresIn),
	}
	if expiresIn > 0 {
		tok.Expiry = m.now().Add(time.Duration(expiresIn) * time.Second)
	}
	return tok.WithExtra(raw), nil
}

func retrieveError(resp *http.Response, body []byte) *oauth2.RetrieveError {
	re := &oauth2.RetrieveError{Response: resp, Body: body}
	var raw map[string]any
	if json.Unmarshal(body, &raw) == nil {
		re.ErrorCode, _ = raw["error"].(string)
		re.ErrorDescription, _ = raw["error_description"].(string)
		re.ErrorURI, _ = raw["error_uri"].(string)
	}
	return re
}

func tokenExpiresInFromRaw(raw map[string]any) int {
	switch v := raw["expires_in"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		if v > 0 {
			return int(v)
		}
	case string:
		if n, err := parseIntString(v); err == nil && n > 0 {
			return int(n)
		}
	}
	return 0
}

func (m *Manager) Refresh(ctx context.Context, ident store.Identity) (string, error) {
	return m.refreshStoredCredential(ctx, ident, false, "rest")
}

func (m *Manager) refreshStoredCredential(
	ctx context.Context,
	ident store.Identity,
	useCurrent bool,
	purpose string,
) (string, error) {
	value, err, _ := m.refreshes.Do(refreshKey(ident, purpose), func() (any, error) {
		return m.refreshCredential(ctx, ident, useCurrent, purpose)
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func refreshKey(ident store.Identity, purpose string) string {
	return strings.ToLower(strings.TrimSpace(ident.Platform)) +
		"\x00" +
		strings.TrimSpace(ident.UserID) +
		"\x00" +
		purpose
}

func (m *Manager) refreshCredential(
	ctx context.Context,
	ident store.Identity,
	useCurrent bool,
	purpose string,
) (string, error) {
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

	meta, err := m.discover(ctx)
	if err != nil {
		return "", err
	}
	targetResource := m.resource(meta)
	if purpose == "mcp" {
		targetResource = m.mcpResource()
	}
	if useCurrent &&
		cred.ExpiresAt.Sub(m.now()) > time.Minute &&
		tokenAudienceMatches(cred.AccessToken, targetResource) {
		return cred.AccessToken, nil
	}

	approved := splitResources(cred.Resource)
	refreshResources := []string{
		approvedRefreshResource(approved, targetResource),
	}
	refreshed, err := m.refresh(ctx, *cred, refreshResources)
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant" {
			if deleteErr := authStore.DeleteCredential(ctx, ident); deleteErr != nil {
				return "", fmt.Errorf("delete rejected OAuth credential: %w", deleteErr)
			}
			return "", ErrNotLoggedIn
		}
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
	if cached, ok := m.cachedMetadata(); ok {
		return cached, nil
	}
	var lastErr error
	server := m.serverURL()
	for _, path := range []string{
		"/.well-known/oauth-authorization-server/api/auth",
		"/api/auth/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
	} {
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
			if err != nil {
				return metadata{}, err
			}
			m.storeMetadata(out)
			return out, nil
		}
		lastErr = fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, responseBodyText(resp))
		_ = resp.Body.Close()
	}
	if cached, ok := m.cachedMetadataStale(); ok {
		return cached, nil
	}
	return metadata{}, fmt.Errorf("could not discover OAuth metadata from %s: %v", m.Server, lastErr)
}

func (m *Manager) cachedMetadata() (metadata, bool) {
	m.metaMu.Lock()
	defer m.metaMu.Unlock()
	if m.metaCache == nil {
		return metadata{}, false
	}
	if m.now().Sub(m.metaAt) > oidcMetadataTTL {
		return metadata{}, false
	}
	return *m.metaCache, true
}

func (m *Manager) cachedMetadataStale() (metadata, bool) {
	m.metaMu.Lock()
	defer m.metaMu.Unlock()
	if m.metaCache == nil {
		return metadata{}, false
	}
	return *m.metaCache, true
}

func (m *Manager) storeMetadata(meta metadata) {
	m.metaMu.Lock()
	defer m.metaMu.Unlock()
	copy := meta
	m.metaCache = &copy
	m.metaAt = m.now()
}

func (m *Manager) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
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

func (m *Manager) resources(meta metadata) []string {
	return splitResources(strings.Join([]string{m.resource(meta), m.mcpResource()}, " "))
}

func (m *Manager) mcpResource() string {
	return m.serverURL() + "/api/mcp"
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
