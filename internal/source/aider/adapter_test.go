package aider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

func installHistory(t *testing.T, root string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, ".aider.chat.history.md")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func aiderEvents(t *testing.T, a *Adapter, s source.Session) []map[string]any {
	t.Helper()
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []map[string]any
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
}

func TestMultiSegmentDuplicateTimestampAndMarkdown(t *testing.T) {
	root := t.TempDir()
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	history = []byte(strings.Replace(string(history), "#### inspect\n", "#### inspect  \n", 1))
	installHistory(t, root, history)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID == got[1].ID || got[0].SnapshotID == got[1].SnapshotID {
		t.Fatalf("segments collide: %#v", got)
	}
	if !reflect.DeepEqual(got[0].Capabilities, []source.Capability{source.CapabilityMessages}) || got[0].Scope.Type != source.ScopeProject || got[0].Scope.Label != filepath.Base(root) {
		t.Fatalf("session=%#v", got[0])
	}
	events := aiderEvents(t, a, got[0])
	want := []map[string]any{{"type": "message", "role": "user", "content": "inspect\nthis garden"}, {"type": "message", "role": "assistant", "content": "I will inspect it."}, {"type": "message", "role": "assistant", "content": "# Notes are preserved"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%#v", events)
	}
}

func TestCRLFNoNewlineMarkerOnlyAndBlankUser(t *testing.T) {
	body := "# aider chat started at 2026-06-01 10:00:00\r\n#### \r\n> state\r\n# aider chat started at 2026-06-01 10:01:00\r\n#### hi\r\nanswer"
	root := t.TempDir()
	installHistory(t, root, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MessageCount != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	events := aiderEvents(t, a, got[0])
	if len(events) != 2 || events[1]["content"] != "answer" {
		t.Fatalf("events=%#v", events)
	}
}

func TestMarkdownContentPreservesMeaningfulWhitespace(t *testing.T) {
	body := []byte("# aider chat started at 2026-06-01 10:00:00\n####   user keeps leading and one trailing \n    indented code  \n    \nplain trailing \n> hidden status\n#### next\nanswer\n")
	root := t.TempDir()
	installHistory(t, root, body)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	want := []map[string]any{
		{"type": "message", "role": "user", "content": "  user keeps leading and one trailing "},
		{"type": "message", "role": "assistant", "content": "    indented code  \n    \nplain trailing "},
		{"type": "message", "role": "user", "content": "next"},
		{"type": "message", "role": "assistant", "content": "answer"},
	}
	if gotEvents := aiderEvents(t, a, got[0]); !reflect.DeepEqual(gotEvents, want) {
		t.Fatalf("events=%#v", gotEvents)
	}
}

func rawSegmentDigests(t *testing.T, data []byte) []string {
	t.Helper()
	marker := []byte(markerPrefix)
	var offsets []int
	for offset := 0; offset < len(data); {
		index := bytes.Index(data[offset:], marker)
		if index < 0 {
			break
		}
		absolute := offset + index
		if absolute == 0 || data[absolute-1] == '\n' {
			offsets = append(offsets, absolute)
		}
		offset = absolute + len(marker)
	}
	var out []string
	for index, start := range offsets {
		end := len(data)
		if index+1 < len(offsets) {
			end = offsets[index+1]
		}
		sum := sha256.Sum256(data[start:end])
		out = append(out, hex.EncodeToString(sum[:]))
	}
	return out
}

func TestSnapshotIDsAreExactRawSegmentDigests(t *testing.T) {
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	root := t.TempDir()
	path := installHistory(t, root, history)
	a := New(root)
	first, err := a.Discover(context.Background())
	if err != nil || len(first) != 2 {
		t.Fatalf("sessions=%#v err=%v", first, err)
	}
	digests := rawSegmentDigests(t, history)
	if len(digests) != 2 || first[0].SnapshotID != digests[0] || first[1].SnapshotID != digests[1] {
		t.Fatalf("snapshots=%q,%q want=%#v", first[0].SnapshotID, first[1].SnapshotID, digests)
	}
	third := []byte("# aider chat started at 2026-06-01 12:00:00\n#### third\nanswer\n")
	withThird := append(append([]byte{}, history...), third...)
	if err := os.WriteFile(path, withThird, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := a.Discover(context.Background())
	if err != nil || len(second) != 3 {
		t.Fatalf("sessions=%#v err=%v", second, err)
	}
	for index := 0; index < 2; index++ {
		if first[index].ID != second[index].ID || first[index].SnapshotID != second[index].SnapshotID {
			t.Fatalf("closed segment %d changed", index)
		}
	}
	thirdDigest := rawSegmentDigests(t, withThird)
	if second[2].SnapshotID != thirdDigest[2] {
		t.Fatalf("third snapshot=%q want=%q", second[2].SnapshotID, thirdDigest[2])
	}
	withTailGrowth := append(append([]byte{}, withThird...), []byte("tail growth  \n")...)
	if err := os.WriteFile(path, withTailGrowth, 0o600); err != nil {
		t.Fatal(err)
	}
	thirdScan, err := a.Discover(context.Background())
	if err != nil || len(thirdScan) != 3 {
		t.Fatalf("sessions=%#v err=%v", thirdScan, err)
	}
	for index := 0; index < 2; index++ {
		if second[index].ID != thirdScan[index].ID || second[index].SnapshotID != thirdScan[index].SnapshotID {
			t.Fatalf("closed segment %d changed on tail growth", index)
		}
	}
	if second[2].ID != thirdScan[2].ID || second[2].SnapshotID == thirdScan[2].SnapshotID || thirdScan[2].SnapshotID != rawSegmentDigests(t, withTailGrowth)[2] {
		t.Fatalf("active tail semantics before=%#v after=%#v", second[2], thirdScan[2])
	}
}

func aggregateHistory(segmentCount, eventsPerSegment int) []byte {
	var body strings.Builder
	for segmentIndex := 0; segmentIndex < segmentCount; segmentIndex++ {
		fmt.Fprintf(&body, "# aider chat started at 2026-06-%02d 10:%02d:00\n", 1+segmentIndex/60, segmentIndex%60)
		for eventIndex := 0; eventIndex < eventsPerSegment; eventIndex++ {
			if eventIndex%2 == 0 {
				fmt.Fprintf(&body, "#### user-%d-%d\n", segmentIndex, eventIndex)
			} else {
				fmt.Fprintf(&body, "assistant-%d-%d\n", segmentIndex, eventIndex)
			}
		}
	}
	return []byte(body.String())
}

func TestAggregateEventLimitFailsWholePhysicalSourceAndClearsAuth(t *testing.T) {
	root := t.TempDir()
	path := installHistory(t, root, aggregateHistory(2, 4))
	a := New(root)
	initial, err := a.Discover(context.Background())
	if err != nil || len(initial) != 2 {
		t.Fatalf("sessions=%#v err=%v", initial, err)
	}
	for _, session := range initial {
		if len(aiderEvents(t, a, session)) != 4 {
			t.Fatalf("session events=%#v", session)
		}
	}
	overLimit := aggregateHistory(512, maxEvents/512+2)
	if len(overLimit) >= maxFileBytes {
		t.Fatalf("test input unexpectedly too large: %d", len(overLimit))
	}
	if err := os.WriteFile(path, overLimit, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	a.mu.RLock()
	known := len(a.known)
	a.mu.RUnlock()
	if known != 0 {
		t.Fatalf("oversized source installed %d authorizations", known)
	}
	if r, err := a.Open(context.Background(), initial[0]); err == nil {
		r.Close()
		t.Fatal("stale authorization survived aggregate limit")
	}
}

func TestAppendAndTailSnapshotSemantics(t *testing.T) {
	first := []byte("# aider chat started at 2026-06-01 10:00:00\n#### one\nanswer\n")
	second := []byte("# aider chat started at 2026-06-01 11:00:00\n#### two\nanswer\n")
	root := t.TempDir()
	path := installHistory(t, root, first)
	a := New(root)
	before, err := a.Discover(context.Background())
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, first...), second...), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), before[0]); err != nil {
		t.Fatalf("closed segment invalidated by append: %v", err)
	} else {
		r.Close()
	}
	after, err := a.Discover(context.Background())
	if err != nil || len(after) != 2 {
		t.Fatal(err)
	}
	if before[0].ID != after[0].ID || before[0].SnapshotID != after[0].SnapshotID {
		t.Fatal("closed segment changed after append")
	}
	tailBefore := after[1]
	if err := os.WriteFile(path, append(append(append([]byte{}, first...), second...), []byte("more\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	tailAfter, _ := a.Discover(context.Background())
	if tailBefore.ID != tailAfter[1].ID || tailBefore.SnapshotID == tailAfter[1].SnapshotID || tailAfter[0].SnapshotID != after[0].SnapshotID {
		t.Fatal("tail semantics violated")
	}
}

func TestExactFileNestedDecoysAndStatusNoTools(t *testing.T) {
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	root := t.TempDir()
	installHistory(t, root, history)
	for _, name := range []string{".aider.input.history", ".aider.llm.history", ".aider.conf.yml", ".env"} {
		if err := os.WriteFile(filepath.Join(root, name), history, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	installHistory(t, filepath.Join(root, "nested"), history)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	for _, s := range got {
		if !reflect.DeepEqual(s.Capabilities, []source.Capability{source.CapabilityMessages}) {
			t.Fatalf("caps=%#v", s.Capabilities)
		}
	}
}

func TestOpenRejectsTamperForgeryCrossInstanceAndInode(t *testing.T) {
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	root := t.TempDir()
	path := installHistory(t, root, history)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	forged := got[0]
	forged.MessageCount++
	if r, err := a.Open(context.Background(), forged); err == nil {
		r.Close()
		t.Fatal("forgery accepted")
	}
	if r, err := New(root).Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("cross instance accepted")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, history, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("same-content inode accepted")
	}
}

func TestSegmentTamperAndLimits(t *testing.T) {
	root := t.TempDir()
	installHistory(t, root, []byte("# aider chat started at 2026-06-01 10:00:00\n#### hi\nanswer\n"))
	a := New(root)
	got, _ := a.Discover(context.Background())
	forged := got[0]
	forged.ID = strings.Replace(forged.ID, "aider:", "aider:x", 1)
	if r, err := a.Open(context.Background(), forged); err == nil {
		r.Close()
		t.Fatal("segment forgery accepted")
	}
	roots := make([]string, 65)
	for i := range roots {
		roots[i] = root
	}
	if _, err := New(roots...).Discover(context.Background()); err == nil {
		t.Fatal("root limit accepted")
	}
}

type cancelContext struct {
	context.Context
	mu           sync.Mutex
	calls, after int
}

func (c *cancelContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}
func (c *cancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func TestLimitsCancellationAndRootDedup(t *testing.T) {
	t.Run("segments", func(t *testing.T) {
		var body strings.Builder
		for i := 0; i <= maxSegments; i++ {
			fmt.Fprintf(&body, "# aider chat started at 2026-06-01 10:00:00\n#### %d\n", i)
		}
		root := t.TempDir()
		installHistory(t, root, []byte(body.String()))
		if got, err := New(root).Discover(context.Background()); err != nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i <= maxDirectoryEntries; i++ {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("decoy-%04d", i)), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := New(root).Discover(context.Background()); err == nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		root := t.TempDir()
		installHistory(t, root, adaptertest.ReadFixture(t, "../testdata/aider/history.md"))
		got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 4})
		if !errors.Is(err, context.Canceled) || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("dedup", func(t *testing.T) {
		root := t.TempDir()
		installHistory(t, root, adaptertest.ReadFixture(t, "../testdata/aider/history.md"))
		got, err := New(root, root).Discover(context.Background())
		if err != nil || len(got) != 2 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
}

func TestAuthorizationContractAndRootSwap(t *testing.T) {
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	root := t.TempDir()
	path := installHistory(t, root, history)
	other := New(root)
	forged, err := other.Discover(context.Background())
	if err != nil || len(forged) == 0 {
		t.Fatal(err)
	}
	a := New(root)
	adaptertest.AuthorizationContract(t, a, func() {
		mutated := []byte(strings.Replace(string(history), "inspect", "change", 1))
		if err := os.WriteFile(path, mutated, 0o600); err != nil {
			t.Fatal(err)
		}
	}, forged[0])

	base := t.TempDir()
	live := filepath.Join(base, "project")
	if err := os.Mkdir(live, 0o755); err != nil {
		t.Fatal(err)
	}
	installHistory(t, live, history)
	swap := New(live)
	sessions, err := swap.Discover(context.Background())
	if err != nil || len(sessions) == 0 {
		t.Fatal(err)
	}
	if err := os.Rename(live, filepath.Join(base, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(live, 0o755); err != nil {
		t.Fatal(err)
	}
	installHistory(t, live, history)
	if r, err := swap.Open(context.Background(), sessions[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}
}

func TestSymlinkHistoryIsIgnored(t *testing.T) {
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	outside := t.TempDir()
	target := installHistory(t, outside, history)
	root := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, fileName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestDefaultHomeScopeIsStablePrivateCollection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installHistory(t, home, adaptertest.ReadFixture(t, "../testdata/aider/history.md"))
	first, err := New().Discover(context.Background())
	if err != nil || len(first) != 2 {
		t.Fatalf("sessions=%#v err=%v", first, err)
	}
	if first[0].Scope.Type != source.ScopeSessionCollection || first[0].Scope.Label != "Aider sessions" || filepath.IsAbs(first[0].Scope.Root) || strings.Contains(first[0].Scope.Label, filepath.Base(home)) {
		t.Fatalf("scope=%#v", first[0].Scope)
	}
	second, err := New().Discover(context.Background())
	if err != nil || second[0].ID != first[0].ID || second[0].Scope.Root != first[0].Scope.Root {
		t.Fatalf("sessions=%#v err=%v", second, err)
	}
}

func TestAuthorizationDoesNotCacheBodiesAndIDsSeparateRoots(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches body in %s", typ.Field(i).Name)
		}
	}
	history := adaptertest.ReadFixture(t, "../testdata/aider/history.md")
	first, second := t.TempDir(), t.TempDir()
	installHistory(t, first, history)
	installHistory(t, second, history)
	got, err := New(first, second).Discover(context.Background())
	if err != nil || len(got) != 4 || got[0].ID == got[2].ID {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
