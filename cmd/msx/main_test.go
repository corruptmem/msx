package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
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
