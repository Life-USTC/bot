package auth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

// VerifiedToken wraps an oauth2.Token and preserves the optional ID token.
type VerifiedToken struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Expiry       time.Time
	ExpiresIn    int
	Scope        string
	IDToken      string
}

func newVerifiedToken(tok *oauth2.Token) *VerifiedToken {
	if tok == nil {
		return nil
	}
	return &VerifiedToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Expiry:       tok.Expiry,
		ExpiresIn:    tokenExpiresIn(tok, 0),
		Scope:        tokenExtraString(tok, "scope"),
		IDToken:      tokenExtraString(tok, "id_token"),
	}
}

func tokenExtraString(tok *oauth2.Token, key string) string {
	if tok == nil {
		return ""
	}
	if s, ok := tok.Extra(key).(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// ValidateIDToken checks the ID token's issuer and audience claims.
// It does not verify the JWT signature; callers should fetch the issuer's
// JWKS and verify the signature when required.
func (t *VerifiedToken) ValidateIDToken(issuer, audience string) error {
	if t == nil || t.IDToken == "" {
		return nil
	}
	parsed, err := jwt.ParseSigned(t.IDToken, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return fmt.Errorf("invalid id_token: %w", err)
	}
	claims := map[string]any{}
	if err := parsed.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return fmt.Errorf("invalid id_token claims: %w", err)
	}
	if issuer != "" {
		if iss, _ := claims["iss"].(string); strings.TrimSpace(iss) != issuer {
			return fmt.Errorf("invalid issuer %q, expected %q", iss, issuer)
		}
	}
	if audience != "" {
		if !audienceMatches(claims["aud"], audience) {
			return fmt.Errorf("invalid audience, expected %q", audience)
		}
	}
	if exp, ok := expiresAtFromClaim(claims["exp"]); ok && !exp.After(time.Now()) {
		return errors.New("id_token expired")
	}
	return nil
}

func audienceMatches(audClaim any, expected string) bool {
	if s, ok := audClaim.(string); ok {
		return strings.TrimSpace(s) == expected
	}
	if list, ok := audClaim.([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && strings.TrimSpace(s) == expected {
				return true
			}
		}
	}
	return false
}

func expiresAtFromClaim(expClaim any) (time.Time, bool) {
	switch v := expClaim.(type) {
	case float64:
		return time.Unix(int64(v), 0), true
	case int:
		return time.Unix(int64(v), 0), true
	case int64:
		return time.Unix(v, 0), true
	case string:
		if n, err := parseIntString(v); err == nil {
			return time.Unix(n, 0), true
		}
	}
	return time.Time{}, false
}

func parseIntString(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func tokenExpiresIn(tok *oauth2.Token, fallback int) int {
	if tok == nil {
		return fallback
	}
	if extra := tok.Extra("expires_in"); extra != nil {
		switch v := extra.(type) {
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
	}
	if !tok.Expiry.IsZero() {
		secs := int(time.Until(tok.Expiry).Seconds())
		if secs > 0 {
			return secs
		}
	}
	return fallback
}

// jsonContentTypeTransport ensures JSON token responses without a Content-Type
// header are still parsed as JSON by golang.org/x/oauth2. Some test fixtures do
// not set the header, and the production server is expected to return JSON.
type jsonContentTypeTransport struct {
	base http.RoundTripper
}

func (t *jsonContentTypeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if len(body) > 0 {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			resp.Header.Set("Content-Type", "application/json")
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func wrapJSONContentTypeTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &jsonContentTypeTransport{base: base}
}

func verifiedTokenToCredential(clientID, resource string, vt *VerifiedToken, fallbackRefresh, fallbackScope string, now time.Time) (store.Credential, error) {
	accessToken := strings.TrimSpace(vt.AccessToken)
	if accessToken == "" {
		return store.Credential{}, errors.New("token response missing access_token")
	}
	expiresIn := vt.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	refreshToken := strings.TrimSpace(vt.RefreshToken)
	if refreshToken == "" {
		refreshToken = fallbackRefresh
	}
	scope := strings.TrimSpace(vt.Scope)
	if scope == "" {
		scope = fallbackScope
	}
	return store.Credential{
		ClientID:     clientID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    strings.TrimSpace(vt.TokenType),
		ExpiresAt:    now.Add(time.Duration(expiresIn) * time.Second),
		Scope:        scope,
		Resource:     resource,
	}, nil
}
