package adaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

type Install func(root string, data []byte) error

func SafetyContract(t *testing.T, newAdapter func(string) source.Adapter, install Install, fixture []byte) {
	t.Helper()
	run := func(name string, data []byte) ([]source.Session, source.Adapter) {
		t.Helper()
		root := t.TempDir()
		if err := install(root, data); err != nil {
			t.Fatal(err)
		}
		a := newAdapter(root)
		got, err := a.Discover(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return got, a
	}
	truncated, _ := run("truncated", append(append([]byte(nil), fixture...), []byte("{\n")...))
	if len(truncated) != 1 || truncated[0].MalformedCount == 0 {
		t.Fatalf("truncated=%#v", truncated)
	}
	overLine := append(append([]byte(nil), fixture...), bytes.Repeat([]byte("x"), (1<<20)+1)...)
	if got, _ := run("line-limit", overLine); len(got) != 0 {
		t.Fatalf("over-line accepted: %#v", got)
	}
	overSession := append(append([]byte(nil), fixture...), bytes.Repeat([]byte{'\n'}, (4<<20)+1)...)
	if got, _ := run("session-limit", overSession); len(got) != 0 {
		t.Fatalf("over-session accepted: %#v", got)
	}
	got, a := run("fixture", fixture)
	if len(got) != 1 {
		t.Fatalf("fixture=%#v", got)
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"cwd", "path", "sessionId", "session_id", "uuid", "parentUuid", "secret"} {
			if _, exists := event[forbidden]; exists {
				t.Fatalf("private top-level field %q in %#v", forbidden, event)
			}
		}
	}
}

func ReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		var value any
		if json.Unmarshal([]byte(line), &value) != nil {
			continue
		}
		if containsAbsolute(value) {
			t.Fatalf("fixture contains absolute path: %s", line)
		}
	}
	return data
}
func containsAbsolute(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.HasPrefix(x, "/") || len(x) > 2 && ((x[0] >= 'A' && x[0] <= 'Z') || (x[0] >= 'a' && x[0] <= 'z')) && x[1] == ':'
	case []any:
		for _, e := range x {
			if containsAbsolute(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range x {
			if containsAbsolute(e) {
				return true
			}
		}
	}
	return false
}
