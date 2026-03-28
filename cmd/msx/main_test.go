package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestRunMailGetAndNextAgainstTestServer(t *testing.T) {
	t.Setenv("MSX_HOME", t.TempDir())
	t.Setenv("MSX_GRAPH_BASE_URL", "")
	seedProfile(t, os.Getenv("MSX_HOME"), "personal")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1.0/me/messages/msg-123":
			if got := r.URL.Query().Get("$select"); !strings.Contains(got, "conversationId") {
				t.Fatalf("expected detail select fields, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg-123","subject":"hello"}`)
		case r.URL.Path == "/v1.0/me/messages":
			if got := r.URL.Query().Get("$skiptoken"); got != "abc" {
				t.Fatalf("expected skiptoken abc, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"value":[{"id":"msg-2"}]}`)
		default:
			t.Fatalf("unexpected request path: %s", r.URL.String())
		}
	}))
	defer server.Close()
	t.Setenv("MSX_GRAPH_BASE_URL", server.URL+"/v1.0")

	stdout, stderr, err := captureRun([]string{"--profile", "personal", "mail-get", "msg-123"})
	if err != nil {
		t.Fatalf("mail-get failed: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"id": "msg-123"`) {
		t.Fatalf("unexpected mail-get stdout: %s", stdout)
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$skiptoken=abc","value":[{"id":"1","subject":"Invoice 1"},{"id":"2","subject":"Dinner"}]}`)
	}))
	defer server.Close()
	t.Setenv("MSX_GRAPH_BASE_URL", server.URL+"/v1.0")

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

func TestEmitTextIsPrettyJSON(t *testing.T) {
	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	if err := emit(globalFlags{format: "text"}, map[string]any{"ok": true}); err != nil {
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
