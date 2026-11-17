package cmdutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/output"
)

// PrintList prints the result of a list-style command like
// clictx.Context.Print (pruned JSON on stdout), except that the top-level
// collections named by keys are always present: an empty one is printed
// as [] instead of being pruned, so that a consumer iterating over it
// (jq '.messages[]') sees "nothing found" rather than a missing field.
//
// A key put back keeps the place it has in v: the position of its struct
// field, or its sorted position in a map.
func PrintList(rt *clictx.Context, v any, keys ...string) error {
	pruned, err := output.Marshal(v)
	if err != nil {
		return err
	}
	out, err := withEmptyLists(v, pruned, keys)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(rt.Stdout, "%s\n", out)
	return err
}

// withEmptyLists returns pruned, the output.Marshal rendering of v, with
// the missing keys put back as empty arrays.
func withEmptyLists(v any, pruned []byte, keys []string) ([]byte, error) {
	present, values, ok := topLevel(pruned)
	if !ok {
		return pruned, nil // not an object: nothing to add a key to
	}
	if !slices.ContainsFunc(keys, func(k string) bool { return !slices.Contains(present, k) }) {
		return pruned, nil
	}
	order, err := keyOrder(v)
	if err != nil {
		return nil, err
	}
	// Keys v does not know (and, defensively, those the pruned output has
	// that the order lacks) go last.
	for _, k := range slices.Concat(present, keys) {
		if !slices.Contains(order, k) {
			order = append(order, k)
		}
	}

	var buf bytes.Buffer
	buf.WriteString("{")
	sep := "\n  "
	for _, k := range order {
		value, kept := values[k]
		switch {
		case kept:
		case slices.Contains(keys, k):
			value = json.RawMessage("[]")
		default:
			continue
		}
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		// value was rendered one level deep already: its own indentation
		// still fits.
		fmt.Fprintf(&buf, "%s%s: %s", sep, name, value)
		sep = ",\n  "
	}
	buf.WriteString("\n}")
	return buf.Bytes(), nil
}

// topLevel splits a JSON object into its keys, in order, and their raw
// values; ok is false when data is not an object.
func topLevel(data []byte) (keys []string, values map[string]json.RawMessage, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	values = map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		key, isString := tok.(string)
		if err != nil || !isString {
			return nil, nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, false
		}
		keys = append(keys, key)
		values[key] = raw
	}
	return keys, values, true
}

// keyOrder lists the top-level JSON keys v can have, in output order:
// the fields of a struct (including those `omitempty` leaves out when
// empty), or the keys v marshals to.
func keyOrder(v any) ([]string, error) {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && t.Kind() == reflect.Struct {
		return structKeys(t), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	keys, _, _ := topLevel(raw)
	return keys, nil
}

func structKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			keys = append(keys, structKeys(f.Type)...)
		case !f.IsExported():
		case name == "":
			keys = append(keys, f.Name)
		default:
			keys = append(keys, name)
		}
	}
	return keys
}
