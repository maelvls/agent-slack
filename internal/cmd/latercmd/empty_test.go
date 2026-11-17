package latercmd

import "testing"

// "items" is printed even when nothing is saved; --counts-only, which
// lists no items by design, still leaves it out.
func TestListWithNothingSaved(t *testing.T) {
	h := newHarness(t)
	h.serve = func(method string, _ map[string]any) map[string]any {
		if method == "saved.list" {
			return map[string]any{"saved_items": []any{}, "counts": map[string]any{"uncompleted_count": 0.0, "archived_count": 0.0, "completed_count": 0.0, "total_count": 0.0}}
		}
		return apiError("unknown_method")
	}
	counts := "  \"counts\": {\n    \"in_progress\": 0,\n    \"archived\": 0,\n    \"completed\": 0,\n    \"total\": 0\n  }"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"list"}, "{\n" + counts + ",\n  \"items\": []\n}\n"},
		{nil, "{\n" + counts + ",\n  \"items\": []\n}\n"},
		{[]string{"list", "--counts-only"}, "{\n" + counts + "\n}\n"},
	} {
		if _, err := h.run(tc.args...); err != nil {
			t.Fatal(err)
		}
		if got := h.stdout.String(); got != tc.want {
			t.Errorf("later %v printed %s, want %s", tc.args, got, tc.want)
		}
	}
}
