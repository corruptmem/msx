package main

import (
	"encoding/json"
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
