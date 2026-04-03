package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corruptmem/msx/internal/graph"
	"github.com/corruptmem/msx/internal/store"
)

func TestParseGlobalsAllowsFlagsBeforeAndAfterCommand(t *testing.T) {
	g, rest, err := parseGlobals([]string{"mail", "--profile", "personal", "--format=text", "--top", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if g.profile != "personal" || g.format != "text" {
		t.Fatalf("unexpected globals: %+v", g)
	}
	if !reflect.DeepEqual(rest, []string{"mail", "--top", "5"}) {
		t.Fatalf("unexpected rest: %#v", rest)
	}
}

func TestParseGlobalsRejectsInvalidFormat(t *testing.T) {
	if _, _, err := parseGlobals([]string{"--format", "yaml", "profiles"}); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestShowUsageHelpPath(t *testing.T) {
	if err := showUsage(false, ""); !errors.Is(err, errHelpShown) {
		t.Fatalf("expected errHelpShown, got %v", err)
	}
}

func TestVersionCommandEmitsBuildMetadata(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	version, commit, buildDate = "v1.2.3", "abc123", "2026-03-28T13:00:00Z"
	defer func() {
		version, commit, buildDate = oldVersion, oldCommit, oldBuildDate
	}()

	stdout, stderr, err := captureRun([]string{"version"})
	if err != nil {
		t.Fatalf("version failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"version": "v1.2.3"`) || !strings.Contains(stdout, `"commit": "abc123"`) || !strings.Contains(stdout, `"build_date": "2026-03-28T13:00:00Z"`) {
		t.Fatalf("unexpected version stdout: %s", stdout)
	}
}

func TestFilterEventsMatchesNestedFieldsCaseInsensitively(t *testing.T) {
	in := []any{
		map[string]any{"subject": "Dentist", "location": map[string]any{"displayName": "Main Street"}},
		map[string]any{"subject": "Lunch", "location": map[string]any{"displayName": "Office"}},
	}
	out := filterEvents(in, "street")
	if len(out) != 1 {
		t.Fatalf("expected one result, got %d", len(out))
	}
	blob, _ := json.Marshal(out[0])
	if string(blob) == "" {
		t.Fatal("expected event payload")
	}
}

func TestFilterMailBySubject(t *testing.T) {
	in := []any{
		map[string]any{"subject": "Monthly Invoice"},
		map[string]any{"subject": "Dinner"},
	}
	out := filterMailBySubject(in, "invoice")
	if len(out) != 1 || out[0]["subject"] != "Monthly Invoice" {
		t.Fatalf("unexpected filtered mail: %#v", out)
	}
}

func TestFilterDriveItems(t *testing.T) {
	in := []any{
		map[string]any{"name": "notes.txt", "file": map[string]any{}},
		map[string]any{"name": "docs", "folder": map[string]any{}},
	}
	files := filterDriveItems(in, "files")
	folders := filterDriveItems(in, "folders")
	if len(files) != 1 || files[0]["name"] != "notes.txt" {
		t.Fatalf("unexpected files filter result: %#v", files)
	}
	if len(folders) != 1 || folders[0]["name"] != "docs" {
		t.Fatalf("unexpected folders filter result: %#v", folders)
	}
}

func TestSplitCSVTrimsAndDropsEmptyValues(t *testing.T) {
	got := splitCSV(" User.Read, , Mail.Read ,")
	want := []string{"User.Read", "Mail.Read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected scopes: got=%#v want=%#v", got, want)
	}
}

func TestRequirePositive(t *testing.T) {
	if err := requirePositive("--top", 0); err == nil {
		t.Fatal("expected validation error")
	}
	if err := requirePositive("--top", 1); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNextLink(t *testing.T) {
	if err := validateNextLink("https://graph.microsoft.com/v1.0/me/messages?$skiptoken=x"); err != nil {
		t.Fatalf("expected valid next link, got %v", err)
	}
	if err := validateNextLink("http://graph.microsoft.com/v1.0/me/messages"); err == nil {
		t.Fatal("expected https validation error")
	}
	if err := validateNextLink("https://evil.example/v1.0/me/messages"); err == nil {
		t.Fatal("expected host validation error")
	}
}

func TestRunDetailCommandsAndNextAgainstTestServer(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	t.Setenv("MSX_GRAPH_BASE_URL", "")
	seedProfile(t, os.Getenv("MSX_HOME"), "personal")

	withGraphHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v1.0/me/messages/msg-123":
			if got := r.URL.Query().Get("$select"); !strings.Contains(got, "conversationId") {
				t.Fatalf("expected message detail select fields, got %q", got)
			}
			return jsonHTTPResponse(http.StatusOK, `{"id":"msg-123","subject":"hello"}`), nil
		case r.URL.Path == "/v1.0/me/contacts/contact-123":
			if got := r.URL.Query().Get("$select"); !strings.Contains(got, "emailAddresses") || !strings.Contains(got, "lastModifiedDateTime") {
				t.Fatalf("expected contact detail select fields, got %q", got)
			}
			return jsonHTTPResponse(http.StatusOK, `{"id":"contact-123","displayName":"Alice"}`), nil
		case r.URL.Path == "/v1.0/sites/site-123":
			if got := r.URL.Query().Get("$select"); !strings.Contains(got, "sharepointIds") || !strings.Contains(got, "webUrl") {
				t.Fatalf("expected site detail select fields, got %q", got)
			}
			return jsonHTTPResponse(http.StatusOK, `{"id":"site-123","displayName":"Docs"}`), nil
		case r.URL.Path == "/v1.0/me/messages":
			if got := r.URL.Query().Get("$skiptoken"); got != "abc" {
				t.Fatalf("expected skiptoken abc, got %q", got)
			}
			return jsonHTTPResponse(http.StatusOK, `{"value":[{"id":"msg-2"}]}`), nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.String())
			return nil, nil
		}
	})
	t.Setenv("MSX_GRAPH_BASE_URL", "https://graph.example.test/v1.0")

	stdout, stderr, err := captureRun([]string{"--profile", "personal", "mail-get", "msg-123"})
	if err != nil {
		t.Fatalf("mail-get failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"id": "msg-123"`) {
		t.Fatalf("unexpected mail-get stdout: %s", stdout)
	}

	stdout, stderr, err = captureRun([]string{"--profile", "personal", "contact-get", "contact-123"})
	if err != nil {
		t.Fatalf("contact-get failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"id": "contact-123"`) {
		t.Fatalf("unexpected contact-get stdout: %s", stdout)
	}

	stdout, stderr, err = captureRun([]string{"--profile", "personal", "site-get", "site-123"})
	if err != nil {
		t.Fatalf("site-get failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"id": "site-123"`) {
		t.Fatalf("unexpected site-get stdout: %s", stdout)
	}

	stdout, stderr, err = captureRun([]string{"--profile", "personal", "next", "--url", "https://graph.microsoft.com/v1.0/me/messages?$skiptoken=abc"})
	if err != nil {
		t.Fatalf("next failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"id": "msg-2"`) {
		t.Fatalf("unexpected next stdout: %s", stdout)
	}
}

func TestRunMailSubjectFilterPreservesTopLevelShape(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	seedProfile(t, os.Getenv("MSX_HOME"), "personal")

	withGraphHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$skiptoken=abc","value":[{"id":"1","subject":"Invoice 1"},{"id":"2","subject":"Dinner"}]}`), nil
	})
	t.Setenv("MSX_GRAPH_BASE_URL", "https://graph.example.test/v1.0")

	stdout, stderr, err := captureRun([]string{"--profile", "personal", "mail", "--subject", "invoice"})
	if err != nil {
		t.Fatalf("mail failed: %v stderr=%s", err, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("failed to decode stdout: %v\n%s", err, stdout)
	}
	if got["@odata.nextLink"] == nil {
		t.Fatalf("expected @odata.nextLink to survive filtering: %+v", got)
	}
	items, _ := got["value"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one filtered item, got %d", len(items))
	}
}

func TestStateExportAndImportCommandsRoundTrip(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	seedProfile(t, os.Getenv("MSX_HOME"), "personal")

	stdout, stderr, err := captureRun([]string{"--profile", "personal", "state-export"})
	if err != nil {
		t.Fatalf("state-export failed: %v stderr=%s", err, stderr)
	}
	backup, err := store.ParseStateBackup([]byte(stdout))
	if err != nil {
		t.Fatalf("state-export did not emit a valid backup: %v\n%s", err, stdout)
	}
	if len(backup.Profiles) != 1 || backup.Profiles[0].Profile.Name != "personal" {
		t.Fatalf("unexpected export payload: %+v", backup)
	}
	if !jsonBlobEqual(backup.Profiles[0].Token.Raw, json.RawMessage(`{"ok":true}`)) {
		t.Fatalf("raw token payload was not preserved: %s", string(backup.Profiles[0].Token.Raw))
	}

	importHome := t.TempDir()
	backupPath := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(backupPath, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MSX_HOME", importHome)

	stdout, stderr, err = captureRun([]string{"state-import", "--in", backupPath})
	if err != nil {
		t.Fatalf("state-import failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"ok": true`) || !strings.Contains(stdout, `"count": 1`) {
		t.Fatalf("unexpected state-import stdout: %s", stdout)
	}

	s, err := store.Open(importHome)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotProfile, err := s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	gotToken, err := s.GetToken("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile.ClientID != "cid" || gotToken.RefreshToken != "rt" {
		t.Fatalf("unexpected restored state: %+v %+v", gotProfile, gotToken)
	}
}

func TestStateImportCommandRefusesOverwriteWithoutFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MSX_HOME", home)
	seedProfile(t, home, "personal")

	backup := store.StateBackup{
		Schema:  "msx-state-backup",
		Version: 1,
		Profiles: []store.StateBackupProfile{{
			Profile: store.Profile{Name: "personal", Authority: "organizations", ClientID: "new"},
			Token:   store.Token{AccessToken: "new-at", RefreshToken: "new-rt", TokenType: "Bearer", Raw: json.RawMessage(`{"new":true}`)},
		}},
	}
	blob, err := store.MarshalStateBackup(backup)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(backupPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := captureRun([]string{"state-import", "--in", backupPath}); err == nil {
		t.Fatal("expected overwrite refusal")
	} else if !strings.Contains(err.Error(), "already exists") && !strings.Contains(stderr, "already exists") {
		t.Fatalf("unexpected error: %v stderr=%s", err, stderr)
	}

	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotProfile, err := s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile.ClientID != "cid" {
		t.Fatalf("profile was overwritten unexpectedly: %+v", gotProfile)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := captureRun([]string{"state-import", "--in", backupPath, "--overwrite"}); err != nil {
		t.Fatalf("overwrite import failed: %v stderr=%s", err, stderr)
	}
	s, err = store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotProfile, err = s.GetProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile.ClientID != "new" {
		t.Fatalf("overwrite import did not apply: %+v", gotProfile)
	}
}

func TestStateImportCommandRejectsMalformedBackup(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	backupPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(backupPath, []byte(`{"schema":"wrong","version":1,"profiles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := captureRun([]string{"state-import", "--in", backupPath}); err == nil {
		t.Fatal("expected malformed backup error")
	} else if !strings.Contains(err.Error(), "unsupported state backup schema") && !strings.Contains(stderr, "unsupported state backup schema") {
		t.Fatalf("unexpected error: %v stderr=%s", err, stderr)
	}
}

func captureRun(args []string) (stdout string, stderr string, err error) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	outR, outW, e := os.Pipe()
	if e != nil {
		return "", "", e
	}
	errR, errW, e := os.Pipe()
	if e != nil {
		return "", "", e
	}
	os.Stdout = outW
	os.Stderr = errW

	runErr := run(args)

	_ = outW.Close()
	_ = errW.Close()
	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	return string(outBytes), string(errBytes), runErr
}

func withGraphHTTPClient(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	old := graph.DefaultHTTPClient
	graph.DefaultHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(fn)}
	}
	t.Cleanup(func() {
		graph.DefaultHTTPClient = old
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func jsonBlobEqual(a, b json.RawMessage) bool {
	var left any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	var right any
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func seedProfile(t *testing.T, dir string, profile string) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveProfileAndToken(store.Profile{Name: profile, Authority: "common", ClientID: "cid", Scopes: []string{"User.Read"}}, store.Token{AccessToken: "token", RefreshToken: "rt", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix(), Raw: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("expected state db to exist: %v", err)
	}
}

func TestEmitTextFallsBackToPrettyJSON(t *testing.T) {
	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	if err := emit(globalFlags{format: "text"}, "contacts", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"ok": true`) {
		t.Fatalf("unexpected text output: %s", buf.String())
	}
}

func TestRenderMailListText(t *testing.T) {
	text, ok := renderText("mail", map[string]any{
		"@odata.nextLink": "https://graph.microsoft.com/v1.0/me/messages?$skiptoken=abc",
		"value": []any{
			map[string]any{
				"subject":          "Invoice ready",
				"receivedDateTime": "2026-03-28T12:00:00Z",
				"isRead":           false,
				"from":             map[string]any{"emailAddress": map[string]any{"address": "billing@example.com"}},
				"webLink":          "https://outlook.office.com/mail/msg-1",
			},
		},
	})
	if !ok {
		t.Fatal("expected renderer to handle mail")
	}
	for _, needle := range []string{"[unread]", "Invoice ready", "billing@example.com", "next: https://graph.microsoft.com"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in output: %s", needle, text)
		}
	}
}

func TestRenderProfilesText(t *testing.T) {
	text, ok := renderText("profiles", []map[string]any{{
		"name":          "personal",
		"authority":     "common",
		"account_email": "cam@example.com",
		"scopes":        []string{"User.Read", "Mail.ReadWrite"},
		"expires_at":    float64(1774700000),
	}})
	if !ok {
		t.Fatal("expected renderer to handle profiles")
	}
	for _, needle := range []string{"personal", "authority: common", "account: cam@example.com", "scopes: User.Read, Mail.ReadWrite", "expires: "} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in output: %s", needle, text)
		}
	}
}

func TestRenderAgendaListText(t *testing.T) {
	text, ok := renderText("agenda", map[string]any{
		"@odata.nextLink": "https://graph.microsoft.com/v1.0/me/calendarView?$skiptoken=abc",
		"value": []any{
			map[string]any{
				"subject":   "Standup",
				"start":     map[string]any{"dateTime": "2026-03-28T09:00:00"},
				"end":       map[string]any{"dateTime": "2026-03-28T09:30:00"},
				"location":  map[string]any{"displayName": "Room 1"},
				"organizer": map[string]any{"emailAddress": map[string]any{"address": "boss@example.com"}},
				"webLink":   "https://outlook.office.com/calendar/event-1",
			},
		},
	})
	if !ok {
		t.Fatal("expected renderer to handle agenda")
	}
	for _, needle := range []string{"Standup", "2026-03-28T09:00:00", "Room 1", "boss@example.com", "next: https://graph.microsoft.com"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in output: %s", needle, text)
		}
	}
}

func TestRenderFilesListText(t *testing.T) {
	text, ok := renderText("files", map[string]any{
		"value": []any{
			map[string]any{
				"name":                 "report.docx",
				"file":                 map[string]any{},
				"size":                 float64(4096),
				"lastModifiedDateTime": "2026-03-27T10:00:00Z",
				"parentReference":      map[string]any{"path": "/drive/root:/Documents"},
				"webUrl":               "https://onedrive.live.com/file-1",
			},
			map[string]any{
				"name":   "Archive",
				"folder": map[string]any{"childCount": float64(3)},
				"webUrl": "https://onedrive.live.com/dir-1",
			},
		},
	})
	if !ok {
		t.Fatal("expected renderer to handle files")
	}
	for _, needle := range []string{"[file]", "report.docx", "4096", "[folder]", "Archive"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in output: %s", needle, text)
		}
	}
}

func TestRenderWhoamiText(t *testing.T) {
	text, ok := renderText("whoami", map[string]any{
		"displayName":       "Cameron Harris",
		"userPrincipalName": "cam@example.com",
		"mail":              "cam@example.com",
		"id":                "user-abc-123",
		"jobTitle":          "Engineer",
		"officeLocation":    "Remote",
		"mobilePhone":       "+1 555 0100",
		"preferredLanguage": "en-US",
	})
	if !ok {
		t.Fatal("expected renderer to handle whoami")
	}
	for _, needle := range []string{"Cameron Harris", "cam@example.com", "user-abc-123", "Engineer", "Remote", "+1 555 0100", "en-US"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in output: %s", needle, text)
		}
	}
}

func TestParseLocationUTC(t *testing.T) {
	loc, err := parseLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	if loc != time.UTC {
		t.Fatalf("expected time.UTC, got %v", loc)
	}
}

func TestParseLocationEmpty(t *testing.T) {
	loc, err := parseLocation("")
	if err != nil {
		t.Fatal(err)
	}
	if loc != time.UTC {
		t.Fatalf("expected time.UTC, got %v", loc)
	}
}

func TestParseLocationValid(t *testing.T) {
	loc, err := parseLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	if loc.String() != "Europe/London" {
		t.Fatalf("unexpected location: %v", loc)
	}
}

func TestParseLocationInvalid(t *testing.T) {
	_, err := parseLocation("Fake/Zone")
	if err == nil {
		t.Fatal("expected error for unknown timezone")
	}
}

func TestConvertTZUTCIdentity(t *testing.T) {
	s := "2026-04-03T10:00:00Z"
	// UTC short-circuits — returns as-is
	got := convertTZ(time.UTC, s)
	if got != s {
		t.Fatalf("expected identity for UTC, got %q", got)
	}
}

func TestConvertTZShiftsOffset(t *testing.T) {
	loc, _ := parseLocation("Europe/London")
	// BST offset is +01:00 from late March
	got := convertTZ(loc, "2026-04-03T10:00:00Z")
	if got != "2026-04-03T11:00:00+01:00" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestConvertTZGraphNoSuffix(t *testing.T) {
	// Graph agenda datetimes have no Z or offset
	loc, _ := parseLocation("Europe/London")
	got := convertTZ(loc, "2026-04-03T10:00:00.0000000")
	// parsed as UTC wall time, shifted to BST
	if got != "2026-04-03T11:00:00+01:00" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestConvertTZUnparseable(t *testing.T) {
	loc, _ := parseLocation("Europe/London")
	s := "not-a-date"
	got := convertTZ(loc, s)
	if got != s {
		t.Fatalf("expected unchanged string for unparseable input, got %q", got)
	}
}

func TestConvertMailTZ(t *testing.T) {
	loc, _ := parseLocation("America/New_York")
	items := []map[string]any{
		{"receivedDateTime": "2026-04-03T15:00:00Z", "subject": "Hello"},
	}
	convertMailTZ(items, loc)
	got := items[0]["receivedDateTime"].(string)
	// UTC-4 in April (EDT)
	if got != "2026-04-03T11:00:00-04:00" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestConvertAgendaTZ(t *testing.T) {
	loc, _ := parseLocation("America/New_York")
	items := []map[string]any{
		{
			"subject": "Standup",
			"start":   map[string]any{"dateTime": "2026-04-03T15:00:00Z", "timeZone": "UTC"},
			"end":     map[string]any{"dateTime": "2026-04-03T15:30:00Z", "timeZone": "UTC"},
		},
	}
	convertAgendaTZ(items, loc)
	start := nestedMap(items[0], "start")["dateTime"].(string)
	end := nestedMap(items[0], "end")["dateTime"].(string)
	if start != "2026-04-03T11:00:00-04:00" {
		t.Fatalf("unexpected start: %q", start)
	}
	if end != "2026-04-03T11:30:00-04:00" {
		t.Fatalf("unexpected end: %q", end)
	}
}
