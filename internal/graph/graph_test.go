package graph

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/corruptmem/msx/internal/store"
)

type stubAuth struct {
	mu          sync.Mutex
	refreshes   int
	forceCalls  int
	activeToken string
}

func (s *stubAuth) RefreshIfNeeded(_ *store.Store, _ string, _ time.Duration) (store.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	return store.Token{AccessToken: s.activeToken, RefreshToken: "rt", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

func (s *stubAuth) ForceRefresh(_ *store.Store, _ string) (store.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forceCalls++
	s.activeToken = "fresh-token"
	return store.Token{AccessToken: s.activeToken, RefreshToken: "rt2", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestRequestForcesRefreshOnUnauthorized(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveProfileAndToken(store.Profile{Name: "p", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}, store.Token{AccessToken: "stale-token", RefreshToken: "rt", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix(), Raw: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}

	auth := &stubAuth{activeToken: "stale-token"}
	var seen []string
	client := Client{
		Store:   s,
		Profile: "p",
		BaseURL: "https://graph.example.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") == "Bearer stale-token" {
				return jsonResponse(http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		})},
		Auth: auth,
	}
	resp, err := client.Request(http.MethodGet, "/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if auth.refreshes != 1 || auth.forceCalls != 1 {
		t.Fatalf("unexpected auth call counts: refresh=%d force=%d", auth.refreshes, auth.forceCalls)
	}
	if len(seen) != 2 || seen[0] != "Bearer stale-token" || seen[1] != "Bearer fresh-token" {
		t.Fatalf("unexpected authorization headers: %#v", seen)
	}
}

func TestRequestAddsConsistencyHeaderForSearch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveProfileAndToken(store.Profile{Name: "p", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}, store.Token{AccessToken: "token", RefreshToken: "rt", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix(), Raw: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}

	auth := &stubAuth{activeToken: "token"}
	client := Client{
		Store:   s,
		Profile: "p",
		BaseURL: "https://graph.example.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("ConsistencyLevel"); got != "eventual" {
				t.Fatalf("expected ConsistencyLevel eventual, got %q", got)
			}
			return jsonResponse(http.StatusOK, `{"value":[]}`), nil
		})},
		Auth: auth,
	}
	if _, err := client.Request(http.MethodGet, "/me/messages", map[string]string{"$search": `"invoice"`}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestURLAddsConsistencyHeaderForSearchNextLink(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveProfileAndToken(store.Profile{Name: "p", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}, store.Token{AccessToken: "token", RefreshToken: "rt", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix(), Raw: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}

	auth := &stubAuth{activeToken: "token"}
	client := Client{
		Store:   s,
		Profile: "p",
		BaseURL: "https://graph.example.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("ConsistencyLevel"); got != "eventual" {
				t.Fatalf("expected ConsistencyLevel eventual, got %q", got)
			}
			return jsonResponse(http.StatusOK, `{"value":[]}`), nil
		})},
		Auth: auth,
	}
	if _, err := client.RequestURL(http.MethodGet, `https://graph.microsoft.com/v1.0/me/messages?$search=%22invoice%22&$skiptoken=abc`); err != nil {
		t.Fatal(err)
	}
}
