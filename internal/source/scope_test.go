package source

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var testScopeSecret = bytes.Repeat([]byte{0x42}, 32)

func mustGroupScopes(t *testing.T, sessions []Session, secret []byte) []Scope {
	t.Helper()
	scopes, err := GroupScopes(sessions, secret)
	if err != nil {
		t.Fatal(err)
	}
	return scopes
}

func TestGroupScopesMergesEquivalentProjectRootsAcrossSources(t *testing.T) {
	sessions := []Session{
		{ID: "claude-1", Product: "claude", OpaqueRef: "/Users/alice/.claude/private", Scope: ScopeRef{Type: ScopeProject, Root: "/Users/alice/work/demo/", Label: "demo"}},
		{ID: "codex-1", Product: "codex", OpaqueRef: "/Users/alice/.codex/private", Scope: ScopeRef{Type: ScopeProject, Root: "/Users/alice/work/demo", Label: "demo"}},
	}

	scopes := mustGroupScopes(t, sessions, testScopeSecret)

	if len(scopes) != 1 {
		t.Fatalf("len(scopes) = %d, want 1: %#v", len(scopes), scopes)
	}
	if scopes[0].SessionCount != 2 {
		t.Fatalf("SessionCount = %d, want 2", scopes[0].SessionCount)
	}
	if got := strings.Join(scopes[0].Products, ","); got != "claude,codex" {
		t.Fatalf("Products = %q, want claude,codex", got)
	}
}

func TestGroupScopesJSONDoesNotLeakPrivateReferences(t *testing.T) {
	scopes := mustGroupScopes(t, []Session{{
		ID:        "secret-session-id",
		Product:   "codex",
		OpaqueRef: "/Users/alice/.codex/private.jsonl",
		Scope:     ScopeRef{Type: ScopeProject, Root: "/Users/alice/work/demo", Label: "demo"},
	}}, testScopeSecret)

	data, err := json.Marshal(scopes)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/Users/alice", "private.jsonl", "secret-session-id", `"root"`, `"opaqueRef"`} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("JSON leaked %q: %s", secret, data)
		}
	}
}

func TestGroupScopesIsDeterministicallySorted(t *testing.T) {
	input := []Session{
		{ID: "2", Product: "codex", Scope: ScopeRef{Type: ScopeWorkspace, Root: "/z", Label: "Zulu"}},
		{ID: "1", Product: "claude", Scope: ScopeRef{Type: ScopeProject, Root: "/a", Label: "Alpha"}},
	}
	first := mustGroupScopes(t, input, testScopeSecret)
	second := mustGroupScopes(t, []Session{input[1], input[0]}, testScopeSecret)

	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("order depends on input:\n%s\n%s", a, b)
	}
}

func TestGroupScopesUsesDeterministicLabelForMergedScope(t *testing.T) {
	firstInput := []Session{
		{ID: "2", Product: "codex", Scope: ScopeRef{Type: ScopeProject, Root: "/work/demo", Label: "Zulu"}},
		{ID: "1", Product: "claude", Scope: ScopeRef{Type: ScopeProject, Root: "/work/demo", Label: "Alpha"}},
	}

	first, _ := json.Marshal(mustGroupScopes(t, firstInput, testScopeSecret))
	second, _ := json.Marshal(mustGroupScopes(t, []Session{firstInput[1], firstInput[0]}, testScopeSecret))

	if string(first) != string(second) {
		t.Fatalf("merged scope depends on input order:\n%s\n%s", first, second)
	}
}

func TestGroupScopesNormalizesCrossPlatformPaths(t *testing.T) {
	tests := []struct {
		name  string
		roots []string
	}{
		{name: "posix", roots: []string{"/Users/alice/work/demo/", "/Users/alice/work/./demo"}},
		{name: "windows drive", roots: []string{`C:\Users\Alice\work\demo`, `c:/users/alice/work/demo/`}},
		{name: "unc", roots: []string{`\\Server\Share\Team\demo`, `//server/share/team/demo/`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := mustGroupScopes(t, []Session{
				{ID: "1", Product: "claude", Scope: ScopeRef{Type: ScopeProject, Root: tt.roots[0]}},
				{ID: "2", Product: "codex", Scope: ScopeRef{Type: ScopeProject, Root: tt.roots[1]}},
			}, testScopeSecret)
			if len(scopes) != 1 {
				t.Fatalf("roots did not normalize together: %#v", scopes)
			}
		})
	}
}

func TestGroupScopesKeepsScopeTypeNamespacesDistinct(t *testing.T) {
	sessions := []Session{
		{ID: "project", Scope: ScopeRef{Type: ScopeProject, Root: "/same"}},
		{ID: "workspace", Scope: ScopeRef{Type: ScopeWorkspace, Root: "/same"}},
		{ID: "group", Scope: ScopeRef{Type: ScopeConversationGroup, Root: "/same"}},
		{ID: "collection", Scope: ScopeRef{Type: ScopeSessionCollection, Root: "/same"}},
	}

	scopes := mustGroupScopes(t, sessions, testScopeSecret)

	if len(scopes) != 4 {
		t.Fatalf("len(scopes) = %d, want 4", len(scopes))
	}
	keys := map[string]bool{}
	for _, scope := range scopes {
		if keys[scope.Key] {
			t.Fatalf("duplicate private key %q", scope.Key)
		}
		keys[scope.Key] = true
	}
}

func TestGroupScopesUsesSecretBoundKeys(t *testing.T) {
	sessions := []Session{{ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: "/private/project"}}}
	first := mustGroupScopes(t, sessions, bytes.Repeat([]byte{1}, 32))
	repeated := mustGroupScopes(t, sessions, bytes.Repeat([]byte{1}, 32))
	otherInstall := mustGroupScopes(t, sessions, bytes.Repeat([]byte{2}, 32))

	if first[0].Key != repeated[0].Key {
		t.Fatalf("same secret produced unstable keys: %q != %q", first[0].Key, repeated[0].Key)
	}
	if first[0].Key == otherInstall[0].Key {
		t.Fatalf("different installation secrets produced linkable key %q", first[0].Key)
	}
}

func TestGroupScopesRejectsMissingOrShortSecret(t *testing.T) {
	for _, secret := range [][]byte{nil, []byte("too-short")} {
		if _, err := GroupScopes(nil, secret); err == nil {
			t.Fatalf("GroupScopes accepted secret of length %d", len(secret))
		}
	}
}

func TestGroupScopesFiltersUnicodeFormatCharactersFromLabel(t *testing.T) {
	scopes := mustGroupScopes(t, []Session{{
		ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: "/work/demo", Label: "safe\u202Etxt"},
	}}, testScopeSecret)
	if got := scopes[0].Label; got != "safetxt" {
		t.Fatalf("label = %q, want safetxt", got)
	}
}

func TestGroupScopesTreatsWindowsAndUNCCaseInsensitively(t *testing.T) {
	tests := []struct {
		name       string
		firstRoot  string
		secondRoot string
	}{
		{name: "drive", firstRoot: `C:\Users\Alice\Work\Demo`, secondRoot: `c:/users/alice/work/demo`},
		{name: "UNC", firstRoot: `\\SERVER\Share\TEAM\Demo`, secondRoot: `//server/share/team/demo`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := mustGroupScopes(t, []Session{
				{ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: tt.firstRoot}},
				{ID: "2", Scope: ScopeRef{Type: ScopeProject, Root: tt.secondRoot}},
			}, testScopeSecret)
			if len(scopes) != 1 {
				t.Fatalf("case variants did not merge: %#v", scopes)
			}
		})
	}
}

func TestGroupScopesCopiesSessionCapabilities(t *testing.T) {
	capabilities := []Capability{"messages"}
	scopes := mustGroupScopes(t, []Session{{
		ID: "1", Capabilities: capabilities, Scope: ScopeRef{Type: ScopeProject, Root: "/work/demo"},
	}}, testScopeSecret)

	capabilities[0] = "mutated"

	if got := scopes[0].Sessions[0].Capabilities[0]; got != "messages" {
		t.Fatalf("scope capability = %q, want independent copy", got)
	}
}

func TestGroupScopesPOSIXBackslashIsNotASeparator(t *testing.T) {
	scopes := mustGroupScopes(t, []Session{
		{ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: `/tmp/a\b`}},
		{ID: "2", Scope: ScopeRef{Type: ScopeProject, Root: `/tmp/a/b`}},
	}, testScopeSecret)
	if len(scopes) != 2 {
		t.Fatalf("POSIX backslash path merged with slash path: %#v", scopes)
	}
}

func TestGroupScopesUNCParentCannotEscapeShareRoot(t *testing.T) {
	scopes := mustGroupScopes(t, []Session{
		{ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: `\\server\share\..\..\other`}},
		{ID: "2", Scope: ScopeRef{Type: ScopeProject, Root: `//SERVER/SHARE/other`}},
	}, testScopeSecret)
	if len(scopes) != 1 {
		t.Fatalf("UNC traversal escaped share root: %#v", scopes)
	}
}

func TestGroupScopesDriveRelativeDoesNotMergeWithAbsoluteDrive(t *testing.T) {
	scopes := mustGroupScopes(t, []Session{
		{ID: "1", Scope: ScopeRef{Type: ScopeProject, Root: `C:foo`}},
		{ID: "2", Scope: ScopeRef{Type: ScopeProject, Root: `C:\foo`}},
	}, testScopeSecret)
	if len(scopes) != 2 {
		t.Fatalf("drive-relative path merged with absolute drive path: %#v", scopes)
	}
}
