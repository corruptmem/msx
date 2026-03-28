package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
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

func TestStateBackupRoundTripSingleAndAllProfiles(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	firstProfile := Profile{Name: "personal", Authority: "common", ClientID: "cid-1", Scopes: []string{"User.Read"}, AccountEmail: "cam@example.com", TenantHint: "common", CreatedAt: 101, UpdatedAt: 202}
	firstToken := Token{AccessToken: "at-1", RefreshToken: "rt-1", TokenType: "Bearer", Scope: "User.Read", ExpiresAt: 303, ObtainedAt: 404, Raw: json.RawMessage(`{"nested":{"x":1}}`)}
	secondProfile := Profile{Name: "work", Authority: "organizations", ClientID: "cid-2", Scopes: []string{"Mail.Read"}, CreatedAt: 505, UpdatedAt: 606}
	secondToken := Token{AccessToken: "at-2", RefreshToken: "rt-2", TokenType: "Bearer", Scope: "Mail.Read", ExpiresAt: 707, ObtainedAt: 808, Raw: json.RawMessage(`{"tenant":"contoso"}`)}
	if err := s.SaveProfileAndToken(firstProfile, firstToken); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfileAndToken(secondProfile, secondToken); err != nil {
		t.Fatal(err)
	}

	single, err := s.ExportProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	if len(single.Profiles) != 1 || single.Profiles[0].Profile.Name != "personal" {
		t.Fatalf("unexpected single export: %+v", single)
	}

	all, err := s.ExportAllProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{all.Profiles[0].Profile.Name, all.Profiles[1].Profile.Name}; !reflect.DeepEqual(got, []string{"personal", "work"}) {
		t.Fatalf("unexpected export order: %#v", got)
	}

	blob, err := MarshalStateBackup(all)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseStateBackup(blob)
	if err != nil {
		t.Fatal(err)
	}

	restore, err := Open(filepath.Join(t.TempDir(), "restore"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore.Close()
	if err := restore.ImportStateBackup(parsed, false); err != nil {
		t.Fatal(err)
	}

	gotProfile, err := restore.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	gotToken, err := restore.GetToken("personal")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotProfile, parsed.Profiles[0].Profile) {
		t.Fatalf("profile mismatch after import:\n got=%+v\nwant=%+v", gotProfile, parsed.Profiles[0].Profile)
	}
	if !tokensEqual(gotToken, parsed.Profiles[0].Token) {
		t.Fatalf("token mismatch after import:\n got=%+v\nwant=%+v", gotToken, parsed.Profiles[0].Token)
	}
}

func TestImportStateBackupRefusesOverwriteWithoutFlag(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	originalProfile := Profile{Name: "personal", Authority: "common", ClientID: "old"}
	originalToken := Token{AccessToken: "old-at", RefreshToken: "old-rt", TokenType: "Bearer", Raw: json.RawMessage(`{"old":true}`)}
	if err := s.SaveProfileAndToken(originalProfile, originalToken); err != nil {
		t.Fatal(err)
	}

	backup := StateBackup{
		Schema:  stateBackupSchema,
		Version: stateBackupVersion,
		Profiles: []StateBackupProfile{{
			Profile: Profile{Name: "personal", Authority: "organizations", ClientID: "new"},
			Token:   Token{AccessToken: "new-at", RefreshToken: "new-rt", TokenType: "Bearer", Raw: json.RawMessage(`{"new":true}`)},
		}},
	}
	if err := s.ImportStateBackup(backup, false); err == nil {
		t.Fatal("expected overwrite refusal")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}

	gotProfile, err := s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	gotToken, err := s.GetToken("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile.ClientID != "old" || gotToken.RefreshToken != "old-rt" {
		t.Fatalf("state changed despite overwrite refusal: %+v %+v", gotProfile, gotToken)
	}

	if err := s.ImportStateBackup(backup, true); err != nil {
		t.Fatal(err)
	}
	gotProfile, err = s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	gotToken, err = s.GetToken("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile.ClientID != "new" || gotToken.RefreshToken != "new-rt" {
		t.Fatalf("overwrite import did not apply: %+v %+v", gotProfile, gotToken)
	}
}

func TestParseStateBackupRejectsMalformedFormat(t *testing.T) {
	cases := []string{
		`{"schema":"wrong","version":1,"profiles":[]}`,
		`{"schema":"msx-state-backup","version":9,"profiles":[]}`,
		`{"schema":"msx-state-backup","version":1,"profiles":[{"profile":{"authority":"common"},"token":{"access_token":"at","refresh_token":"rt","raw":{}}}]}`,
		`{"schema":"msx-state-backup","version":1,"profiles":[{"profile":{"name":"p"},"token":{"access_token":"at","refresh_token":"","raw":{}}}]}`,
		`{"schema":"msx-state-backup","version":1,"profiles":[],"extra":true}`,
		`{"schema":"msx-state-backup","version":1,"profiles":[]} trailing`,
	}
	for _, tc := range cases {
		if _, err := ParseStateBackup([]byte(tc)); err == nil {
			t.Fatalf("expected malformed backup rejection for %s", tc)
		}
	}
}

func tokensEqual(a, b Token) bool {
	if a.AccessToken != b.AccessToken || a.RefreshToken != b.RefreshToken || a.TokenType != b.TokenType || a.Scope != b.Scope || a.ExpiresAt != b.ExpiresAt || a.ObtainedAt != b.ObtainedAt {
		return false
	}
	var rawA any
	if err := json.Unmarshal(a.Raw, &rawA); err != nil {
		return false
	}
	var rawB any
	if err := json.Unmarshal(b.Raw, &rawB); err != nil {
		return false
	}
	return reflect.DeepEqual(rawA, rawB)
}
