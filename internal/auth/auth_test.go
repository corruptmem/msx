package auth

import (
	"encoding/json"
	"testing"
)

func TestTokenFromJSONRequiresRefreshAndAccess(t *testing.T) {
	_, err := tokenFromJSON([]byte(`{"access_token":"a"}`), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokenFromJSONRoundTrip(t *testing.T) {
	tok, err := tokenFromJSON([]byte(`{"access_token":"a","refresh_token":"r","token_type":"Bearer","scope":"User.Read","expires_in":3600}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "a" || tok.RefreshToken != "r" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	var raw map[string]any
	if err := json.Unmarshal(tok.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["refresh_token"] != "r" {
		t.Fatal("raw payload missing refresh token")
	}
}

func TestTokenFromJSONFallsBackToExistingRefreshToken(t *testing.T) {
	tok, err := tokenFromJSON([]byte(`{"access_token":"a2","token_type":"Bearer","scope":"User.Read","expires_in":3600}`), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "old-refresh" {
		t.Fatalf("expected fallback refresh token, got %q", tok.RefreshToken)
	}
	var raw map[string]any
	if err := json.Unmarshal(tok.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["refresh_token"] != "old-refresh" {
		t.Fatalf("expected refresh_token to be preserved in raw payload, got %#v", raw["refresh_token"])
	}
}

func TestParseOAuthError(t *testing.T) {
	oe := parseOAuthError(assertErr(`{"error":"authorization_pending","error_description":"still waiting"}`))
	if oe.Error != "authorization_pending" {
		t.Fatalf("unexpected oauth error: %+v", oe)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func assertErr(v string) error { return errString(v) }
