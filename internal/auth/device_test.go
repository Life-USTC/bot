package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
