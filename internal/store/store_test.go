package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSaveAndGetProfileToken(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	profile := Profile{Name: "personal", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}
	token := Token{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: time.Now().Add(time.Hour).Unix(), ObtainedAt: time.Now().Unix(), Raw: json.RawMessage(`{"x":1}`)}
	if err := s.SaveProfileAndToken(profile, token); err != nil {
		t.Fatal(err)
	}
	gotP, err := s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	gotT, err := s.GetToken("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotP.ClientID != "cid" || gotT.RefreshToken != "rt" {
		t.Fatalf("unexpected roundtrip: %+v %+v", gotP, gotT)
	}
}

func TestRefreshIfNeededSerializesWriters(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	profile := Profile{Name: "p", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}
	token := Token{AccessToken: "old-a", RefreshToken: "old-r", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: time.Now().Add(-time.Minute).Unix(), ObtainedAt: time.Now().Unix(), Raw: json.RawMessage(`{"old":true}`)}
	if err := s.SaveProfileAndToken(profile, token); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	calls := 0
	var mu sync.Mutex
	refreshFn := func(Profile, Token) (Token, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		return Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: time.Now().Add(time.Hour).Unix(), ObtainedAt: time.Now().Unix(), Raw: json.RawMessage(fmt.Sprintf(`{"call":%d}`, n))}, nil
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.RefreshIfNeeded("p", 5*time.Minute, refreshFn); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("expected one refresh call, got %d", calls)
	}
}

func TestForceRefreshAlwaysRefreshes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	profile := Profile{Name: "p", Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}
	token := Token{AccessToken: "still-good", RefreshToken: "rt", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: time.Now().Add(time.Hour).Unix(), ObtainedAt: time.Now().Unix(), Raw: json.RawMessage(`{"old":true}`)}
	if err := s.SaveProfileAndToken(profile, token); err != nil {
		t.Fatal(err)
	}

	calls := 0
	next, err := s.ForceRefresh("p", func(Profile, Token) (Token, error) {
		calls++
		return Token{AccessToken: "new", RefreshToken: "new-rt", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: time.Now().Add(2 * time.Hour).Unix(), ObtainedAt: time.Now().Unix(), Raw: json.RawMessage(`{"forced":true}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one force refresh call, got %d", calls)
	}
	if next.AccessToken != "new" {
		t.Fatalf("unexpected token: %+v", next)
	}
	stored, err := s.GetToken("p")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "new" || stored.RefreshToken != "new-rt" {
		t.Fatalf("force refresh was not persisted: %+v", stored)
	}
}
