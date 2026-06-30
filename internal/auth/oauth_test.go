package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func mustSignIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestValidateIDToken(t *testing.T) {
	issuer := "https://auth.example"
	audience := "https://api.example"
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	valid := mustSignIDToken(t, map[string]any{
		"iss": issuer,
		"aud": audience,
		"exp": now.Add(time.Hour).Unix(),
		"sub": "user-1",
	})

	cases := []struct {
		name      string
		token     *VerifiedToken
		issuer    string
		audience  string
		now       time.Time
		wantError string
	}{
		{
			name:     "missing id_token returns nil",
			token:    &VerifiedToken{},
			issuer:   issuer,
			audience: audience,
		},
		{
			name:     "nil receiver returns nil",
			token:    nil,
			issuer:   issuer,
			audience: audience,
		},
		{
			name:     "valid token",
			token:    &VerifiedToken{IDToken: valid},
			issuer:   issuer,
			audience: audience,
			now:      now,
		},
		{
			name: "valid token with array audience",
			token: &VerifiedToken{IDToken: mustSignIDToken(t, map[string]any{
				"iss": issuer,
				"aud": []string{audience, "other"},
				"exp": now.Add(time.Hour).Unix(),
			})},
			issuer:   issuer,
			audience: audience,
			now:      now,
		},
		{
			name: "wrong issuer",
			token: &VerifiedToken{IDToken: mustSignIDToken(t, map[string]any{
				"iss": "https://other.example",
				"aud": audience,
				"exp": now.Add(time.Hour).Unix(),
			})},
			issuer:    issuer,
			audience:  audience,
			now:       now,
			wantError: "invalid issuer",
		},
		{
			name: "wrong audience string",
			token: &VerifiedToken{IDToken: mustSignIDToken(t, map[string]any{
				"iss": issuer,
				"aud": "https://other.example",
				"exp": now.Add(time.Hour).Unix(),
			})},
			issuer:    issuer,
			audience:  audience,
			now:       now,
			wantError: "invalid audience",
		},
		{
			name: "wrong audience array",
			token: &VerifiedToken{IDToken: mustSignIDToken(t, map[string]any{
				"iss": issuer,
				"aud": []string{"https://other.example"},
				"exp": now.Add(time.Hour).Unix(),
			})},
			issuer:    issuer,
			audience:  audience,
			now:       now,
			wantError: "invalid audience",
		},
		{
			name: "expired token",
			token: &VerifiedToken{IDToken: mustSignIDToken(t, map[string]any{
				"iss": issuer,
				"aud": audience,
				"exp": now.Add(-time.Hour).Unix(),
			})},
			issuer:    issuer,
			audience:  audience,
			now:       now,
			wantError: "id_token expired",
		},
		{
			name:      "malformed jwt",
			token:     &VerifiedToken{IDToken: "not-a-jwt"},
			issuer:    issuer,
			audience:  audience,
			wantError: "invalid id_token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.token.ValidateIDToken(tc.issuer, tc.audience, tc.now)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %q, want %q", err.Error(), tc.wantError)
			}
		})
	}
}

func TestValidateIDTokenSkipsEmptyChecks(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	tok := mustSignIDToken(t, map[string]any{
		"iss": "https://auth.example",
		"aud": "https://api.example",
		"exp": now.Add(time.Hour).Unix(),
	})
	vt := &VerifiedToken{IDToken: tok}
	if err := vt.ValidateIDToken("", "", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAudienceMatches(t *testing.T) {
	cases := []struct {
		claim    any
		expected string
		want     bool
	}{
		{"https://api.example", "https://api.example", true},
		{" https://api.example ", "https://api.example", true},
		{"https://other.example", "https://api.example", false},
		{[]any{"https://api.example", "https://other.example"}, "https://api.example", true},
		{[]any{"https://other.example"}, "https://api.example", false},
		{[]any{123}, "https://api.example", false},
		{123, "https://api.example", false},
		{nil, "https://api.example", false},
	}
	for _, tc := range cases {
		got := audienceMatches(tc.claim, tc.expected)
		if got != tc.want {
			t.Errorf("audienceMatches(%#v, %q) = %v, want %v", tc.claim, tc.expected, got, tc.want)
		}
	}
}

func TestExpiresAtFromClaim(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	want := time.Unix(now.Unix(), 0)
	cases := []struct {
		claim any
		want  time.Time
		ok    bool
	}{
		{float64(now.Unix()), want, true},
		{int(now.Unix()), want, true},
		{int64(now.Unix()), want, true},
		{fmt.Sprintf("%d", now.Unix()), want, true},
		{"not-a-number", time.Time{}, false},
		{nil, time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := expiresAtFromClaim(tc.claim)
		if ok != tc.ok {
			t.Errorf("expiresAtFromClaim(%#v) ok = %v, want %v", tc.claim, ok, tc.ok)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("expiresAtFromClaim(%#v) = %v, want %v", tc.claim, got, tc.want)
		}
	}
}
