package output

import "testing"

func TestMarshalPrunesAndKeepsOrder(t *testing.T) {
	type inner struct {
		Z string   `json:"z"`
		A []string `json:"a"`
	}
	v := struct {
		Zeta  string         `json:"zeta"`
		Alpha *int           `json:"alpha"`
		Blank string         `json:"blank"`
		In    inner          `json:"in"`
		Empty map[string]any `json:"empty"`
		Num   int            `json:"num"`
		False bool           `json:"false"`
		URL   string         `json:"url"`
		Arr   []any          `json:"arr"`
	}{Zeta: "z", Blank: "  ", In: inner{Z: "", A: []string{"", "x"}}, URL: "https://a?b=1&c=<2>", Arr: []any{nil, map[string]any{}}}
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "zeta": "z",
  "in": {
    "a": [
      "x"
    ]
  },
  "num": 0,
  "false": false,
  "url": "https://a?b=1&c=<2>"
}`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarshalEmpty(t *testing.T) {
	for _, v := range []any{nil, map[string]any{}, map[string]any{"a": nil}, []string{}} {
		got, err := Marshal(v)
		if err != nil || string(got) != "{}" {
			t.Fatalf("Marshal(%v) = %q, %v", v, got, err)
		}
	}
}

func TestMarshalBigNumber(t *testing.T) {
	got, _ := Marshal(map[string]any{"n": float64(1700000000), "id": int64(9007199254740993)})
	want := "{\n  \"id\": 9007199254740993,\n  \"n\": 1700000000\n}"
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}
