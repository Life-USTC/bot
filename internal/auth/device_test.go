package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

var authTestNow = time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         oauthScope,
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

func TestCredentialFromTokenBodyAcceptsStringExpiresIn(t *testing.T) {
	now := authTestNow
	cred, err := credentialFromTokenBodyAt("client", "resource", []byte(`{
		"access_token": "access",
		"expires_in": "120"
	}`), "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(120 * time.Second); !cred.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", cred.ExpiresAt, want)
	}
}

func TestCredentialFromTokenBodyDefaultsExpiresIn(t *testing.T) {
	now := authTestNow
	cred, err := credentialFromTokenBodyAt("client", "resource", []byte(`{"access_token": "access"}`), "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !cred.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", cred.ExpiresAt, want)
	}
}

func TestCredentialFromTokenBodyTrimsTokenStrings(t *testing.T) {
	cred, err := credentialFromTokenBody("client", "resource", []byte(`{
		"access_token": " access ",
		"refresh_token": "   ",
		"token_type": " Bearer ",
		"scope": "   "
	}`), "fallback-refresh", "fallback-scope")
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
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

func TestRefreshIfUnauthorized(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         oauthScope,
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
