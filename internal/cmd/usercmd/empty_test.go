package usercmd

import "testing"

// "users" is printed even when the page is empty.
func TestListWithNoUsers(t *testing.T) {
	all := members
	members = []any{map[string]any{"id": "U000000002", "name": "deploybot", "is_bot": true, "deleted": false}}
	t.Cleanup(func() { members = all })

	h := newHarness(t)
	if _, err := h.run("list"); err != nil {
		t.Fatal(err)
	}
	if got, want := h.stdout.String(), "{\n  \"users\": []\n}\n"; got != want {
		t.Errorf("printed %s, want %s", got, want)
	}
}
