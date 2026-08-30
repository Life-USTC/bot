package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/Life-USTC/Bot/internal/message"
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
	"account.client-activity:read",
	"workspace.todo:read",
	"workspace.todo:write",
	"workspace.homework:read",
	"workspace.homework:write",
	"workspace.subscription:read",
	"workspace.subscription:write",
	"workspace.calendar-feed:read",
	"workspace.calendar:read",
	"community.comment:read",
	"community.comment:write",
	"community.description:read",
	"community.description:write",
	"community.user:read",
	"community.section-homework:read",
	"community.section-homework:write",
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
	discovers  singleflight.Group

	metaMu    sync.Mutex
	metaCache *metadata
	metaAt    time.Time
}

const oidcMetadataTTL = 30 * time.Minute

type tokenPurpose string

const (
	purposeREST tokenPurpose = "rest"
	purposeMCP  tokenPurpose = "mcp"
)

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
	Status     store.LoginStatus
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
	return textutil.TrimTrailingSlash(value)
}

func resourceURLsMatch(left, right string) bool {
	return normalizeResourceURL(left) == normalizeResourceURL(right)
}

func tokenAudienceValues(accessToken string) []string {
	aud, ok := accessTokenAudienceClaim(accessToken)
	if !ok {
		return nil
	}
	return audienceValues(aud)
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

func (m *Manager) targetResource(meta metadata, purpose tokenPurpose) string {
	if purpose == purposeMCP {
		return m.mcpResource()
	}
	return m.resource(meta)
}

func approvedRefreshResource(approved []string, resource string) (string, bool) {
	for _, candidate := range approved {
		if resourceURLsMatch(candidate, resource) {
			return normalizeResourceURL(candidate), true
		}
	}
	return "", false
}

func (m *Manager) PollDeviceLogin(ctx context.Context, ident store.Identity) (PollResult, error) {
	return m.pollDeviceLogin(ctx, ident, false)
}

// PollDeviceLoginAndNotify is for the background poller. Terminal results are
// atomically published to the durable outbox with the login transition.
func (m *Manager) PollDeviceLoginAndNotify(ctx context.Context, ident store.Identity) (PollResult, error) {
	return m.pollDeviceLogin(ctx, ident, true)
}

func (m *Manager) pollDeviceLogin(ctx context.Context, ident store.Identity, notify bool) (PollResult, error) {
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
		return m.finishLogin(ctx, ident, *session, store.LoginStatusExpired, nil, "验证码已过期。发送：登录", notify)
	}

	meta, err := m.discover(ctx)
	if err != nil {
		return PollResult{}, err
	}

	// DeviceAccessToken uses real time for its internal deadline; convert the
	// manager-clock-relative expiration to a real future time.
	remaining := session.ExpiresAt.Sub(m.now())
	if remaining <= 0 {
		return m.finishLogin(ctx, ident, *session, store.LoginStatusExpired, nil, "验证码已过期。发送：登录", notify)
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
		return m.mapPollError(ctx, ident, *session, err, notify)
	}

	issuer := m.expectedIssuer(meta)
	audience := session.ClientID
	vt := newVerifiedToken(tok)
	if err := vt.ValidateIDToken(issuer, audience, m.now()); err != nil {
		return PollResult{}, err
	}

	cred, err := verifiedTokenToCredential(session.ClientID, joinResources(resources), vt, "", oauthScope, m.now())
	if err != nil {
		return PollResult{}, err
	}
	return m.finishLogin(ctx, ident, *session, store.LoginStatusApproved, &cred, "登录完成。", notify)
}

func (m *Manager) finishLogin(
	ctx context.Context,
	ident store.Identity,
	session store.LoginSession,
	status store.LoginStatus,
	credential *store.Credential,
	resultMessage string,
	notify bool,
) (PollResult, error) {
	transition := store.LoginTransition{Status: status, Credential: credential}
	if notify && store.HasConversationTarget(ident) {
		sum := sha256.Sum256([]byte(session.DeviceCode))
		outbound := message.Outbound{
			Kind: "login_result",
			Target: message.Conversation{
				Platform: ident.Platform,
				Type:     ident.ConversationType,
				ID:       ident.ConversationID,
			},
			Content: message.Content{Text: resultMessage},
			DedupeKey: fmt.Sprintf(
				"login:%s:%s:%s:%x:%s",
				strings.ToLower(strings.TrimSpace(ident.Platform)),
				strings.ToLower(strings.TrimSpace(ident.ConversationType)),
				strings.TrimSpace(ident.ConversationID),
				sum[:12],
				status,
			),
		}
		transition.Outbound = &outbound
	}
	transitioned, err := m.Store.TransitionLoginSession(ctx, ident, session.DeviceCode, transition)
	if err != nil {
		return PollResult{}, err
	}
	if !transitioned {
		return PollResult{Message: "登录状态已由系统处理，请留意最新消息。"}, nil
	}
	return PollResult{
		Authorized: status == store.LoginStatusApproved,
		Status:     status,
		Message:    resultMessage,
	}, nil
}

func (m *Manager) AccessToken(ctx context.Context, ident store.Identity) (string, error) {
	return m.accessTokenForResource(ctx, ident, purposeREST)
}

func (m *Manager) MCPAccessToken(ctx context.Context, ident store.Identity) (string, error) {
	return m.accessTokenForResource(ctx, ident, purposeMCP)
}

// HasCurrentScopes reports whether the stored OAuth grant includes every scope
// requested by this Bot version. It lets callers distinguish a stale grant that
// needs one reauthorization from a current grant that the server has rejected
// for another reason.
func (m *Manager) HasCurrentScopes(ctx context.Context, ident store.Identity) (bool, error) {
	authStore, err := m.requireStore()
	if err != nil {
		return false, err
	}
	cred, err := authStore.Credential(ctx, ident)
	if err != nil {
		return false, err
	}
	if cred == nil {
		return false, ErrNotLoggedIn
	}
	granted := make(map[string]struct{})
	for _, scope := range strings.Fields(cred.Scope) {
		granted[scope] = struct{}{}
	}
	for _, required := range strings.Fields(oauthScope) {
		if _, ok := granted[required]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (m *Manager) accessTokenForResource(
	ctx context.Context,
	ident store.Identity,
	purpose tokenPurpose,
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
	targetResource := m.targetResource(meta, purpose)

	if cred.ExpiresAt.Sub(m.now()) > time.Minute &&
		tokenAudienceMatches(cred.AccessToken, targetResource) {
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	return m.refreshStoredCredential(ctx, ident, purpose)
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
var ErrReauthorizationRequired = errors.New("reauthorization required")

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
	return m.refreshStoredCredential(ctx, ident, purposeREST)
}

func (m *Manager) refreshStoredCredential(
	ctx context.Context,
	ident store.Identity,
	purpose tokenPurpose,
) (string, error) {
	// REST and MCP share one rotating refresh token, so serialize them by user.
	value, err, _ := m.refreshes.Do(refreshKey(ident), func() (any, error) {
		return m.refreshCredential(ctx, ident, purpose)
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func refreshKey(ident store.Identity) string {
	return strings.ToLower(strings.TrimSpace(ident.Platform)) +
		"\x00" +
		strings.TrimSpace(ident.UserID)
}

func (m *Manager) refreshCredential(
	ctx context.Context,
	ident store.Identity,
	purpose tokenPurpose,
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
	targetResource := m.targetResource(meta, purpose)

	approvedResources := splitResources(cred.Resource)
	_, resourceApproved := approvedRefreshResource(approvedResources, targetResource)
	if !resourceApproved {
		if deleteErr := authStore.DeleteCredential(ctx, ident); deleteErr != nil {
			return "", fmt.Errorf("delete credential missing approved resource: %w", deleteErr)
		}
		if purpose == purposeMCP {
			return "", fmt.Errorf("%w: %s", ErrReauthorizationRequired, targetResource)
		}
		return "", ErrNotLoggedIn
	}
	// Keep the replacement access token usable for every approved audience so
	// callers for another purpose can share this refresh result safely.
	refreshed, err := m.refresh(ctx, *cred, approvedResources)
	if err != nil {
		if errors.Is(err, ErrReauthorizationRequired) {
			if deleteErr := authStore.DeleteCredential(ctx, ident); deleteErr != nil {
				return "", fmt.Errorf("delete credential requiring reauthorization: %w", deleteErr)
			}
			return "", err
		}
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
	if cached, ok := m.cachedMetadata(false); ok {
		return cached, nil
	}
	value, err, _ := m.discovers.Do(m.serverURL(), func() (any, error) {
		if cached, ok := m.cachedMetadata(false); ok {
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
		if cached, ok := m.cachedMetadata(true); ok {
			return cached, nil
		}
		return metadata{}, fmt.Errorf("could not discover OAuth metadata from %s: %v", m.Server, lastErr)
	})
	if err != nil {
		return metadata{}, err
	}
	return value.(metadata), nil
}

func (m *Manager) cachedMetadata(allowStale bool) (metadata, bool) {
	m.metaMu.Lock()
	defer m.metaMu.Unlock()
	if m.metaCache == nil {
		return metadata{}, false
	}
	if !allowStale && m.now().Sub(m.metaAt) > oidcMetadataTTL {
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
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
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

func (m *Manager) mapPollError(ctx context.Context, ident store.Identity, session store.LoginSession, err error, notify bool) (PollResult, error) {
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
			return m.finishLogin(ctx, ident, session, store.LoginStatusExpired, nil, "验证码已过期。发送：登录", notify)
		case "invalid_grant":
			return m.finishLogin(ctx, ident, session, store.LoginStatusInvalid, nil, "登录已失效。发送：登录", notify)
		case "access_denied":
			return m.finishLogin(ctx, ident, session, store.LoginStatusDenied, nil, "登录已取消。发送：登录", notify)
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
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		switch retrieveErr.ErrorCode {
		case "invalid_target", "invalid_scope", "unauthorized_client":
			return fmt.Errorf("%w: %s", ErrReauthorizationRequired, retrieveErr.ErrorCode)
		}
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
