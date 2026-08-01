package adaptertest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestAuthorizationContractCoversForgedMutationAndCancellation(t *testing.T) {
	discovered := source.Session{ID: "synthetic", Product: "fake", OpaqueRef: "token-one"}
	other := &contractAdapter{session: source.Session{ID: "synthetic-other", Product: "fake", OpaqueRef: "token-from-another-instance"}}
	otherSessions, err := other.Discover(context.Background())
	if err != nil || len(otherSessions) != 1 {
		t.Fatal("synthetic second-instance discovery failed")
	}
	adapter := &contractAdapter{session: discovered}
	AuthorizationContract(t, adapter, func() { adapter.mutated = true }, otherSessions[0])
	if !adapter.sawCanceledDiscover || !adapter.sawCanceledOpen {
		t.Fatalf("cancellation checks: discover=%v open=%v", adapter.sawCanceledDiscover, adapter.sawCanceledOpen)
	}
	if adapter.openedForged != 1 || adapter.openedAfterMutation != 1 {
		t.Fatalf("authorization checks: forged=%d mutation=%d", adapter.openedForged, adapter.openedAfterMutation)
	}
	if adapter.openedValid != 1 {
		t.Fatalf("valid opens=%d", adapter.openedValid)
	}
}

func TestAssertCanonicalEventsAcceptsAllowedPrivateFreeEvents(t *testing.T) {
	input := strings.NewReader("{\"type\":\"message\",\"content\":{\"text\":\"synthetic\",\"relative\":\"work/file.go\"}}\n")
	AssertCanonicalEvents(t, input, map[string]bool{"message": true})
}

func TestInspectPrivateFieldsRejectsTopLevelPrivateFieldsAndNestedAbsolutePaths(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "top-level private field", value: map[string]any{"type": "message", "cwd": "relative"}},
		{name: "nested Unix absolute path", value: map[string]any{"content": []any{map[string]any{"text": "/private/synthetic"}}}},
		{name: "nested Windows absolute path", value: map[string]any{"content": map[string]any{"text": `C:\synthetic\secret`}}},
		{name: "nested Windows rooted path", value: map[string]any{"content": map[string]any{"text": `\Users\synthetic\secret`}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := inspectPrivateFields(test.value)
			if err == nil {
				t.Fatal("private value accepted")
			}
			for _, private := range []string{"/private/synthetic", `C:\synthetic\secret`, `\Users\synthetic\secret`} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("private value leaked in failure")
				}
			}
		})
	}
	if err := inspectPrivateFields(map[string]any{"type": "message", "content": []any{"relative/path", "synthetic"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicHelperFailureDoesNotLeakPrivateInput(t *testing.T) {
	if mode := os.Getenv("ADAPTERTEST_FAILURE_HELPER"); mode != "" {
		privatePath := os.Getenv("ADAPTERTEST_PRIVATE_PATH")
		secret := os.Getenv("ADAPTERTEST_PRIVATE_SECRET")
		value := map[string]any{"type": "message", "content": map[string]any{"path_value": privatePath, "text": secret}}
		if mode == "canonical" {
			encoded := `{"type":"message","content":{"path_value":` + strconv.Quote(privatePath) + `,"text":` + strconv.Quote(secret) + `}}` + "\n"
			AssertCanonicalEvents(t, strings.NewReader(encoded), map[string]bool{"message": true})
			return
		}
		AssertNoPrivateFields(t, value)
		return
	}

	privatePath := filepath.Join(t.TempDir(), "private-session.jsonl")
	secret := "unique-private-event-body-7fbb2f"
	for _, mode := range []string{"canonical", "private"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestPublicHelperFailureDoesNotLeakPrivateInput$", "-test.v")
			command.Env = append(os.Environ(),
				"ADAPTERTEST_FAILURE_HELPER="+mode,
				"ADAPTERTEST_PRIVATE_PATH="+privatePath,
				"ADAPTERTEST_PRIVATE_SECRET="+secret,
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("helper subprocess unexpectedly passed")
			}
			if bytes.Contains(output, []byte(privatePath)) || bytes.Contains(output, []byte(secret)) {
				t.Fatal("helper subprocess leaked private input")
			}
		})
	}
}

type contractAdapter struct {
	session             source.Session
	mutated             bool
	sawCanceledDiscover bool
	sawCanceledOpen     bool
	openedForged        int
	openedAfterMutation int
	openedValid         int
}

func (*contractAdapter) Product() string                   { return "fake" }
func (*contractAdapter) Capabilities() []source.Capability { return nil }

func (a *contractAdapter) Discover(ctx context.Context) ([]source.Session, error) {
	if err := ctx.Err(); err != nil {
		a.sawCanceledDiscover = true
		return nil, err
	}
	return []source.Session{a.session}, nil
}

func (a *contractAdapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		a.sawCanceledOpen = true
		return nil, err
	}
	if !reflect.DeepEqual(session, a.session) {
		a.openedForged++
		return nil, errors.New("synthetic unauthorized session")
	}
	if a.mutated {
		a.openedAfterMutation++
		return nil, errors.New("synthetic changed session")
	}
	a.openedValid++
	return io.NopCloser(strings.NewReader("{\"type\":\"message\"}\n")), nil
}
