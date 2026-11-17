package slacktext

import (
	"encoding/json"
	"reflect"
	"testing"
)

// object parses a JSON object literal, as the Slack API would return it.
func object(t *testing.T, literal string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(literal), &out); err != nil {
		t.Fatalf("bad JSON in test: %v\n%s", err, literal)
	}
	return out
}

// assertJSON checks that got marshals to the same JSON value as want.
func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(raw, &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("bad JSON in test: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", raw, want)
	}
}

func assertEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}
