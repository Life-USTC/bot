package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

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
	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
	session, err := manager.BeginDeviceLogin(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if session.UserCode != "USER-CODE" {
		t.Fatalf("session = %#v", session)
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
	if err := s.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode:      "device",
		UserCode:        "USER-CODE",
		ClientID:        "client",
		ExpiresAt:       time.Now().Add(time.Minute),
		IntervalSeconds: 5,
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
	_, err = manager.PollDeviceLogin(ctx, ident)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestCredentialFromTokenBodyAcceptsStringExpiresIn(t *testing.T) {
	before := time.Now()
	cred, err := credentialFromTokenBody("client", "resource", []byte(`{
		"access_token": "access",
		"expires_in": "120"
	}`), "", "")
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(cred.ExpiresAt)
	if remaining < 110*time.Second || remaining > 130*time.Second {
		t.Fatalf("expires_at = %s, before = %s, remaining = %s", cred.ExpiresAt, before, remaining)
	}
}

func TestCredentialFromTokenBodyDefaultsExpiresIn(t *testing.T) {
	cred, err := credentialFromTokenBody("client", "resource", []byte(`{"access_token": "access"}`), "", "")
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(cred.ExpiresAt)
	if remaining < 3500*time.Second || remaining > 3700*time.Second {
		t.Fatalf("expires_at = %s, remaining = %s", cred.ExpiresAt, remaining)
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
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}

	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
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
}
