package adaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

type Install func(root string, data []byte) error

// AuthorizationContract verifies that authorization is instance-local,
// snapshot-bound, and context-aware. forged should be a session discovered by
// another adapter instance or a deliberately altered session.
func AuthorizationContract(t *testing.T, adapter source.Adapter, mutate func(), forged source.Session) {
	t.Helper()
	sessions, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal("authorization contract: discovery failed")
	}
	if len(sessions) == 0 {
		t.Fatal("authorization contract: discovery returned no sessions")
	}
	reader, err := adapter.Open(context.Background(), sessions[0])
	if err != nil {
		if reader != nil {
			reader.Close()
		}
		t.Fatal("authorization contract: discovered session rejected")
	}
	if reader == nil {
		t.Fatal("authorization contract: discovered session rejected")
	}
	if err := reader.Close(); err != nil {
		t.Fatal("authorization contract: discovered session close failed")
	}
	reader, err = adapter.Open(context.Background(), forged)
	if reader != nil {
		reader.Close()
		t.Fatal("authorization contract: forged open returned reader")
	}
	if err == nil {
		t.Fatal("authorization contract: forged session accepted")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Discover(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("authorization contract: canceled discovery accepted")
	}
	reader, err = adapter.Open(canceled, sessions[0])
	if reader != nil {
		reader.Close()
		t.Fatal("authorization contract: canceled open returned reader")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("authorization contract: canceled open accepted")
	}

	if mutate == nil {
		t.Fatal("authorization contract: nil mutation")
	}
	mutate()
	reader, err = adapter.Open(context.Background(), sessions[0])
	if reader != nil {
		reader.Close()
		t.Fatal("authorization contract: changed open returned reader")
	}
	if err == nil {
		t.Fatal("authorization contract: changed session accepted")
	}
}

// AssertCanonicalEvents validates a JSON event stream without including event
// bodies in failure output.
func AssertCanonicalEvents(t *testing.T, r io.Reader, allowed map[string]bool) {
	t.Helper()
	decoder := json.NewDecoder(r)
	for index := 0; ; index++ {
		var event any
		if err := decoder.Decode(&event); err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("canonical event %d: invalid JSON", index)
		}
		object, ok := event.(map[string]any)
		if !ok {
			t.Fatalf("canonical event %d: object required", index)
		}
		typeName, ok := object["type"].(string)
		if !ok || typeName == "" || !allowed[typeName] {
			t.Fatalf("canonical event %d: disallowed type", index)
		}
		if err := inspectPrivateFields(object); err != nil {
			t.Fatalf("canonical event %d: %v", index, err)
		}
	}
}

// AssertNoPrivateFields rejects source-private top-level fields and absolute
// paths at any nesting depth.
func AssertNoPrivateFields(t *testing.T, value any) {
	t.Helper()
	if err := inspectPrivateFields(value); err != nil {
		t.Fatal(err)
	}
}

func inspectPrivateFields(value any) error {
	if object, ok := value.(map[string]any); ok {
		for _, field := range []string{
			"cwd", "path", "sessionId", "session_id", "uuid", "parentUuid", "parent_uuid",
			"secret", "opaqueRef", "opaque_ref", "snapshotId", "snapshot_id",
		} {
			if _, exists := object[field]; exists {
				return fmt.Errorf("private top-level field %q", field)
			}
		}
	}
	if containsAbsolute(value) {
		return errors.New("absolute path in event data")
	}
	return nil
}

func SafetyContract(t *testing.T, newAdapter func(string) source.Adapter, install Install, fixture []byte) {
	t.Helper()
	run := func(name string, data []byte) ([]source.Session, source.Adapter) {
		t.Helper()
		root := t.TempDir()
		if err := install(root, data); err != nil {
			t.Fatalf("%s: fixture installation failed", name)
		}
		a := newAdapter(root)
		got, err := a.Discover(context.Background())
		if err != nil {
			t.Fatalf("%s: discovery failed", name)
		}
		return got, a
	}
	truncated, _ := run("truncated", append(append([]byte(nil), fixture...), []byte("{\n")...))
	if len(truncated) != 1 || truncated[0].MalformedCount == 0 {
		t.Fatalf("truncated: sessions=%d malformed=%v", len(truncated), len(truncated) == 1 && truncated[0].MalformedCount > 0)
	}
	overLine := append(append([]byte(nil), fixture...), bytes.Repeat([]byte("x"), (1<<20)+1)...)
	if got, _ := run("line-limit", overLine); len(got) != 0 {
		t.Fatalf("line-limit: accepted %d sessions", len(got))
	}
	overSession := append(append([]byte(nil), fixture...), bytes.Repeat([]byte{'\n'}, (4<<20)+1)...)
	if got, _ := run("session-limit", overSession); len(got) != 0 {
		t.Fatalf("session-limit: accepted %d sessions", len(got))
	}
	got, a := run("fixture", fixture)
	if len(got) != 1 {
		t.Fatalf("fixture: sessions=%d", len(got))
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal("fixture: open failed")
	}
	defer r.Close()
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("fixture: invalid canonical JSON")
		}
		for _, forbidden := range []string{"cwd", "path", "sessionId", "session_id", "uuid", "parentUuid", "secret"} {
			if _, exists := event[forbidden]; exists {
				t.Fatalf("fixture: private top-level field %q", forbidden)
			}
		}
	}
}

func ReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture read failed")
	}
	for _, line := range strings.Split(string(data), "\n") {
		var value any
		if json.Unmarshal([]byte(line), &value) != nil {
			continue
		}
		if containsAbsolute(value) {
			t.Fatal("fixture contains absolute path")
		}
	}
	return data
}
func containsAbsolute(v any) bool {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "/") || strings.HasPrefix(x, `\`) || windowsDrivePath(x) {
			return true
		}
		if len(x) < len("file:") || !strings.EqualFold(x[:len("file:")], "file:") {
			return false
		}
		uri, err := url.Parse(x)
		if err != nil || !strings.EqualFold(uri.Scheme, "file") {
			return true
		}
		if uri.Host != "" {
			return true
		}
		encodedPath := uri.Path
		if encodedPath == "" {
			encodedPath = uri.Opaque
		}
		decodedPath, err := url.PathUnescape(encodedPath)
		if err != nil {
			return true
		}
		return strings.HasPrefix(decodedPath, "/") || strings.HasPrefix(decodedPath, `\`) || windowsDrivePath(decodedPath)
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

func windowsDrivePath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}
