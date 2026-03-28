package auth

import (
	"encoding/json"
	"testing"
)

func TestTokenFromJSONRequiresRefreshAndAccess(t *testing.T) {
	_, err := tokenFromJSON([]byte(`{"access_token":"a"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokenFromJSONRoundTrip(t *testing.T) {
	tok, err := tokenFromJSON([]byte(`{"access_token":"a","refresh_token":"r","token_type":"Bearer","scope":"User.Read","expires_in":3600}`))
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
