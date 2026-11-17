// Package jsonx has small helpers for walking loosely-typed JSON
// (map[string]any / []any) as returned by the Slack API.
package jsonx

import (
	"encoding/json"
	"math"
	"strconv"
)

// Map returns v as a JSON object, or nil if it is not one.
func Map(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Slice returns v as a JSON array, or nil if it is not one.
func Slice(v any) []any {
	s, _ := v.([]any)
	return s
}

// Maps returns the JSON objects contained in array v, skipping other values.
func Maps(v any) []map[string]any {
	var out []map[string]any
	for _, e := range Slice(v) {
		if m := Map(e); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// Str returns v if it is a string, else "".
func Str(v any) string {
	s, _ := v.(string)
	return s
}

// StrOK returns v if it is a string.
func StrOK(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// Bool returns v if it is a bool, else false.
func Bool(v any) bool {
	b, _ := v.(bool)
	return b
}

// Num returns v as a float64 if it is a JSON number.
func Num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Int returns v as an int if it is a finite JSON number, truncating.
func Int(v any) (int, bool) {
	f, ok := Num(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int(f), true
}

// Get walks nested objects: Get(resp, "channel", "id").
func Get(v any, path ...string) any {
	for _, k := range path {
		m := Map(v)
		if m == nil {
			return nil
		}
		v = m[k]
	}
	return v
}

// GetStr is Str(Get(v, path...)).
func GetStr(v any, path ...string) string { return Str(Get(v, path...)) }

// GetMap is Map(Get(v, path...)).
func GetMap(v any, path ...string) map[string]any { return Map(Get(v, path...)) }

// GetSlice is Slice(Get(v, path...)).
func GetSlice(v any, path ...string) []any { return Slice(Get(v, path...)) }

// Stringish renders a string or number as a string ("" otherwise). Slack
// sometimes returns timestamps and counts as either.
func Stringish(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	if f, ok := Num(v); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}
