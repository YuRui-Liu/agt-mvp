package upload

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

type unstableMarshaler string

func (unstableMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"unstable"`), nil }

func TestCanonicalBytesAreStableAndNormalizePackage(t *testing.T) {
	pkg := Package{
		SchemaVersion: 2,
		Client:        Client{Name: "kuai", Version: "1.0.0", Platform: "darwin-arm64"},
		Scope:         Scope{Type: "project", Key: "scope-key", Label: "campus"},
		Sessions: []Session{{
			ID:     "codex:1",
			Source: Source{Product: "codex", FormatVersion: "v1", AdapterVersion: "1.0"},
			Events: []map[string]any{{"z": json.Number("2"), "a": "first"}},
		}},
		CreatedAt: time.Date(2026, 7, 30, 9, 0, 0, 123456789, time.FixedZone("CST", 8*60*60)),
	}
	first, err := CanonicalBytes(pkg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalBytes(pkg)
	if err != nil || string(first) != string(second) {
		t.Fatalf("canonical output changed: %q / %q (%v)", first, second, err)
	}
	if strings.Contains(string(first), "null") {
		t.Fatalf("nil slices not normalized: %s", first)
	}
	if !strings.Contains(string(first), `"created_at":"2026-07-30T01:00:00.123456789Z"`) {
		t.Fatalf("time not UTC: %s", first)
	}
	if !strings.Contains(string(first), `"a":"first","z":2`) {
		t.Fatalf("map keys not ordered: %s", first)
	}
}

func TestCanonicalBytesRejectsUnstableValues(t *testing.T) {
	funcPkg := func(value any) Package {
		return Package{SchemaVersion: 2, Sessions: []Session{{Events: []map[string]any{{"bad": value}}}}}
	}
	for name, value := range map[string]any{
		"map-any-any": map[any]any{"x": 1},
		"nan":         math.NaN(),
		"infinity":    math.Inf(1),
		"function":    func() {},
		"custom":      unstableMarshaler("custom"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalBytes(funcPkg(value)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDigestIsLowercaseSHA256OfExactBytes(t *testing.T) {
	pkg := Package{SchemaVersion: 2, CreatedAt: time.Unix(0, 0)}
	body, err := CanonicalBytes(pkg)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, err := Digest(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(body)) || digest != "0ced5c5ac804767773bbc4bb963de66e4d48c34b2ab984279b930dde4200db33" {
		t.Fatalf("digest=%q size=%d bytes=%q", digest, size, body)
	}
}
