package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

var authTestNow = time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestManagerWithoutStoreReturnsConfiguredError(t *testing.T) {
	ctx := context.Background()
	manager := Manager{}
	ident := store.Identity{Platform: "napcat", UserID: "42"}

	if _, err := manager.BeginDeviceLogin(ctx, ident); !errors.Is(err, ErrStoreNotConfigured) {
		t.Fatalf("BeginDeviceLogin error = %v", err)
	}
	if _, err := manager.PollDeviceLogin(ctx, ident); !errors.Is(err, ErrStoreNotConfigured) {
		t.Fatalf("PollDeviceLogin error = %v", err)
	}
	if _, err := manager.AccessToken(ctx, ident); !errors.Is(err, ErrStoreNotConfigured) {
		t.Fatalf("AccessToken error = %v", err)
	}
	if err := manager.Logout(ctx, ident); !errors.Is(err, ErrStoreNotConfigured) {
		t.Fatalf("Logout error = %v", err)
	}
	if _, err := manager.Refresh(ctx, ident); !errors.Is(err, ErrStoreNotConfigured) {
		t.Fatalf("Refresh error = %v", err)
	}
	if token, ok := manager.RefreshIfUnauthorized(ctx, ident, life.HTTPError{StatusCode: http.StatusUnauthorized}); ok || token != "" {
		t.Fatalf("RefreshIfUnauthorized token = %q, ok = %v", token, ok)
	}
}

func TestDeviceLoginFlow(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"token_endpoint":                serverURL + "/token",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["client_name"] != "life-ustc-onebot" || body["token_endpoint_auth_method"] != "none" {
			t.Fatalf("registration body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device",
			"user_code":                 "USER-CODE",
			"verification_uri":          serverURL + "/verify",
			"verification_uri_complete": serverURL + "/verify?user_code=USER-CODE",
			"expires_in":                600,
			"interval":                  5,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("device_code") != "device" {
			t.Fatalf("device_code = %q", r.Form.Get("device_code"))
		}
		w.Header().Set("Content-Type", "application/json")
		idToken := mustSignIDToken(t, map[string]any{
			"iss": serverURL,
			"aud": "client",
			"exp": authTestNow.Add(time.Hour).Unix(),
			"sub": "user-1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         oauthScope,
			"id_token":      idToken,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42"}
	now := authTestNow
	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(now)}
	session, err := manager.BeginDeviceLogin(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if session.UserCode != "USER-CODE" {
		t.Fatalf("session = %#v", session)
	}
	if want := now.Add(600 * time.Second); !session.ExpiresAt.Equal(want) {
		t.Fatalf("session expires_at = %s, want %s", session.ExpiresAt, want)
	}
	result, err := manager.PollDeviceLogin(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Authorized {
		t.Fatalf("result = %#v", result)
	}
	token, err := manager.AccessToken(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if token != "access" {
		t.Fatalf("token = %q", token)
	}
}

func TestBeginDeviceLoginUsesContextForDeviceRequest(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "device",
				"user_code":        "USER-CODE",
				"verification_uri": serverURL + "/verify",
				"expires_in":       600,
			})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = manager.BeginDeviceLogin(ctx, store.Identity{Platform: "napcat", UserID: "42"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BeginDeviceLogin error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("BeginDeviceLogin ignored context, elapsed = %s", elapsed)
	}
}

func TestBeginDeviceLoginTrimsServerURL(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "device",
			"user_code":        "USER-CODE",
			"verification_uri": serverURL + "/verify",
			"expires_in":       600,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	manager := Manager{Server: " " + server.URL + "/// ", HTTPClient: server.Client(), Store: s}
	if _, err := manager.BeginDeviceLogin(context.Background(), store.Identity{Platform: "napcat", UserID: "42"}); err != nil {
		t.Fatal(err)
	}
}

func TestPollDeviceLoginExpiresSessionUsingManagerClock(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	now := authTestNow
	ident := store.Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode:      "device",
		UserCode:        "USER-CODE",
		ClientID:        "client",
		ExpiresAt:       now.Add(-time.Second),
		IntervalSeconds: 5,
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{Store: s, Now: fixedClock(now)}
	result, err := manager.PollDeviceLogin(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "验证码已过期。发送：登录" {
		t.Fatalf("result = %#v", result)
	}
	session, err := s.ActiveLoginSession(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("session still active = %#v", session)
	}
}

func TestBeginDeviceLoginRejectsIncompleteDeviceResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "missing device code",
			body: map[string]any{
				"user_code":        "USER-CODE",
				"verification_uri": "https://example.test/verify",
				"expires_in":       600,
			},
			want: "device_code",
		},
		{
			name: "missing verification uri",
			body: map[string]any{
				"device_code": "device",
				"user_code":   "USER-CODE",
				"expires_in":  600,
			},
			want: "verification_uri",
		},
		{
			name: "missing expiry",
			body: map[string]any{
				"device_code":      "device",
				"user_code":        "USER-CODE",
				"verification_uri": "https://example.test/verify",
			},
			want: "expires_in",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var serverURL string
			mux := http.NewServeMux()
			mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{
					"device_authorization_endpoint": serverURL + "/device",
					"registration_endpoint":         serverURL + "/register",
				})
			})
			mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
			})
			mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.body)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			serverURL = server.URL

			s, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()

			ctx := context.Background()
			ident := store.Identity{Platform: "napcat", UserID: "42"}
			manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
			_, err = manager.BeginDeviceLogin(ctx, ident)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			session, err := s.ActiveLoginSession(ctx, ident)
			if err != nil {
				t.Fatal(err)
			}
			if session != nil {
				t.Fatalf("session = %#v", session)
			}
		})
	}
}

func TestBeginDeviceLoginTrimsErrorBody(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("  device failed\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
	_, err = manager.BeginDeviceLogin(context.Background(), store.Identity{Platform: "napcat", UserID: "42"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "device failed") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestResourceTrimsIssuerAndServer(t *testing.T) {
	manager := Manager{Server: " https://life.example/server/ "}
	if got := manager.resource(metadata{Issuer: " https://life.example/issuer/ "}); got != "https://life.example/issuer" {
		t.Fatalf("issuer resource = %q", got)
	}
	if got := manager.resource(metadata{}); got != "https://life.example/server" {
		t.Fatalf("server resource = %q", got)
	}
}

func TestResponseBodyTextReportsReadError(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(errReader{})}

	if got := responseBodyText(resp); !strings.Contains(got, "read response body: read failed") {
		t.Fatalf("responseBodyText = %q", got)
	}
}

func TestBodyTextTruncatesByRune(t *testing.T) {
	got := bodyText([]byte(strings.Repeat("错", 201)))
	if !utf8.ValidString(got) {
		t.Fatalf("bodyText returned invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 200 {
		t.Fatalf("rune count = %d", utf8.RuneCountInString(got))
	}
	if got != strings.Repeat("错", 200) {
		t.Fatalf("bodyText = %q", got)
	}
}

func TestDiscoverReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("metadata unavailable"))
	}))
	defer server.Close()

	manager := Manager{Server: server.URL}
	_, err := manager.discover(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	for _, want := range []string{"/.well-known/openid-configuration", "502", "metadata unavailable"} {
		if !strings.Contains(message, want) {
			t.Fatalf("discover error = %q, want %q", message, want)
		}
	}
}

func TestPollDeviceLoginRejectsInvalidErrorJSON(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`not json`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42"}
	now := authTestNow
	if err := s.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode:      "device",
		UserCode:        "USER-CODE",
		ClientID:        "client",
		ExpiresAt:       now.Add(time.Minute),
		IntervalSeconds: 5,
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(now)}
	_, err = manager.PollDeviceLogin(ctx, ident)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestPollDeviceLoginReturnsBodyReadError(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42"}
	now := authTestNow
	if err := s.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode:      "device",
		UserCode:        "USER-CODE",
		ClientID:        "client",
		ExpiresAt:       now.Add(time.Minute),
		IntervalSeconds: 5,
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{
		Server: "https://life.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/token" {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"token_endpoint":"https://life.test/token"
				}`)),
			}, nil
		})},
		Store: s,
		Now:   fixedClock(now),
	}
	_, err = manager.PollDeviceLogin(ctx, ident)
	if err == nil || !strings.Contains(err.Error(), "token poll response read failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifiedTokenToCredentialAcceptsStringExpiresIn(t *testing.T) {
	now := authTestNow
	cred, err := verifiedTokenToCredential("client", "resource", &VerifiedToken{
		AccessToken: "access",
		ExpiresIn:   120,
	}, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(120 * time.Second); !cred.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", cred.ExpiresAt, want)
	}
}

func TestVerifiedTokenToCredentialDefaultsExpiresIn(t *testing.T) {
	now := authTestNow
	cred, err := verifiedTokenToCredential("client", "resource", &VerifiedToken{
		AccessToken: "access",
	}, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !cred.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", cred.ExpiresAt, want)
	}
}

func TestVerifiedTokenToCredentialTrimsTokenStrings(t *testing.T) {
	cred, err := verifiedTokenToCredential("client", "resource", &VerifiedToken{
		AccessToken:  " access ",
		RefreshToken: "   ",
		TokenType:    " Bearer ",
		Scope:        "   ",
	}, "fallback-refresh", "fallback-scope", authTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "access" {
		t.Fatalf("access_token = %q", cred.AccessToken)
	}
	if cred.RefreshToken != "fallback-refresh" {
		t.Fatalf("refresh_token = %q", cred.RefreshToken)
	}
	if cred.TokenType != "Bearer" {
		t.Fatalf("token_type = %q", cred.TokenType)
	}
	if cred.Scope != "fallback-scope" {
		t.Fatalf("scope = %q", cred.Scope)
	}
}

func TestVerifiedTokenToCredentialRequiresAccessToken(t *testing.T) {
	_, err := verifiedTokenToCredential("client", "resource", &VerifiedToken{}, "", "", authTestNow)
	if err == nil || !strings.Contains(err.Error(), "missing access_token") {
		t.Fatalf("error = %v", err)
	}
}

func TestAccessTokenRefreshThresholdUsesManagerClock(t *testing.T) {
	var serverURL string
	refreshRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.Header().Set("Content-Type", "application/json")
		idToken := mustSignIDToken(t, map[string]any{
			"iss": serverURL,
			"aud": "client",
			"exp": authTestNow.Add(time.Hour).Unix(),
			"sub": "user-1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"id_token":      idToken,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42"}
	now := authTestNow
	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(now)}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		ExpiresAt:    now.Add(2 * time.Minute),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	token, err := manager.AccessToken(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if token != "old-access" || refreshRequests != 0 {
		t.Fatalf("token = %q, refreshRequests = %d", token, refreshRequests)
	}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		ExpiresAt:    now.Add(30 * time.Second),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	token, err = manager.AccessToken(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if token != "new-access" || refreshRequests != 1 {
		t.Fatalf("token = %q, refreshRequests = %d", token, refreshRequests)
	}
}

func TestRefreshReturnsBodyReadError(t *testing.T) {
	manager := Manager{
		Server: "https://life.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/token" {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"token_endpoint":"https://life.test/token"
				}`)),
			}, nil
		})},
		Now: fixedClock(authTestNow),
	}
	_, err := manager.refresh(context.Background(), store.Credential{
		ClientID:     "client",
		RefreshToken: "refresh",
	})
	if err == nil || !strings.Contains(err.Error(), "refresh response read failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshIfUnauthorized(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		idToken := mustSignIDToken(t, map[string]any{
			"iss": serverURL,
			"aud": "client",
			"exp": authTestNow.Add(time.Hour).Unix(),
			"sub": "user-1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         oauthScope,
			"id_token":      idToken,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42"}
	now := authTestNow
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    now.Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(now)}
	if token, ok := manager.RefreshIfUnauthorized(ctx, ident, life.HTTPError{StatusCode: http.StatusInternalServerError}); ok || token != "" {
		t.Fatalf("non-401 refresh = %q, %v", token, ok)
	}
	token, ok := manager.RefreshIfUnauthorized(ctx, ident, life.HTTPError{StatusCode: http.StatusUnauthorized})
	if !ok || token != "new-access" {
		t.Fatalf("refresh = %q, %v", token, ok)
	}
	cred, err := s.Credential(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.AccessToken != "new-access" || cred.RefreshToken != "new-refresh" {
		t.Fatalf("credential = %#v", cred)
	}
	if want := now.Add(time.Hour); !cred.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", cred.ExpiresAt, want)
	}
}

func TestWithRefreshRetriesUnauthorized(t *testing.T) {
	ctx := context.Background()
	var serverURL string
	refreshRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.Header().Set("Content-Type", "application/json")
		idToken := mustSignIDToken(t, map[string]any{
			"iss": serverURL,
			"aud": "client",
			"exp": authTestNow.Add(time.Hour).Unix(),
			"sub": "user-1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      idToken,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    authTestNow.Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	manager := &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(authTestNow)}
	got, err := WithRefresh(ctx, manager, ident, "old-access", func(token string) (string, error) {
		calls = append(calls, token)
		if token == "old-access" {
			return "", life.HTTPError{StatusCode: http.StatusUnauthorized}
		}
		return "ok:" + token, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok:new-access" || refreshRequests != 1 || strings.Join(calls, ",") != "old-access,new-access" {
		t.Fatalf("got = %q, refreshRequests = %d, calls = %#v", got, refreshRequests, calls)
	}
}

func TestWithRefreshVoidRetriesUnauthorized(t *testing.T) {
	ctx := context.Background()
	var serverURL string
	refreshRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.Header().Set("Content-Type", "application/json")
		idToken := mustSignIDToken(t, map[string]any{
			"iss": serverURL,
			"aud": "client",
			"exp": authTestNow.Add(time.Hour).Unix(),
			"sub": "user-1",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      idToken,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    authTestNow.Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	manager := &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s, Now: fixedClock(authTestNow)}
	err = WithRefreshVoid(ctx, manager, ident, "old-access", func(token string) error {
		calls = append(calls, token)
		if token == "old-access" {
			return life.HTTPError{StatusCode: http.StatusUnauthorized}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshRequests != 1 || strings.Join(calls, ",") != "old-access,new-access" {
		t.Fatalf("refreshRequests = %d, calls = %#v", refreshRequests, calls)
	}
}
