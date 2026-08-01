package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndOpenStorage(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "storage/session/project/session-1.json"), `{"id":"session-1","directory":"/workspace/campus-app","time":{"created":1000,"updated":2000}}`)
	write(filepath.Join(root, "storage/message/session-1/msg-user.json"), `{"id":"msg-user","sessionID":"session-1","role":"user","time":{"created":1000}}`)
	write(filepath.Join(root, "storage/part/msg-user/p1.json"), `{"id":"p1","sessionID":"session-1","messageID":"msg-user","type":"text","text":"hello","time":{"created":1000}}`)
	write(filepath.Join(root, "storage/message/session-1/msg-assistant.json"), `{"id":"msg-assistant","sessionID":"session-1","role":"assistant","modelID":"m","time":{"created":2000}}`)
	write(filepath.Join(root, "storage/part/msg-assistant/p2.json"), `{"id":"p2","sessionID":"session-1","messageID":"msg-assistant","type":"tool","tool":"bash","callID":"call-1","state":{"status":"completed","input":{"command":"go test"},"output":"ok"},"time":{"created":2000}}`)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != "opencode:session-1" || got[0].Scope.Root != "/workspace/campus-app" {
		t.Fatal(got[0])
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, types := json.NewDecoder(r), []string{}
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		types = append(types, e["type"].(string))
	}
	if len(types) != 3 || types[1] != "tool_use" || types[2] != "tool_result" {
		t.Fatalf("types=%v", types)
	}
}

func TestOpenRejectsContentReplacementUntilRediscovery(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sp := filepath.Join(root, "storage/session/p/s.json")
	mp := filepath.Join(root, "storage/message/s/m.json")
	pp := filepath.Join(root, "storage/part/m/p.json")
	write(sp, `{"id":"s","directory":"/workspace/p"}`)
	write(mp, `{"id":"m","sessionID":"s","role":"user"}`)
	write(pp, `{"id":"p","sessionID":"s","messageID":"m","type":"text","text":"one"}`)
	a := New(root)
	ss, err := a.Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatal(ss, err)
	}
	write(pp, `{"id":"p","sessionID":"s","messageID":"m","type":"text","text":"two"}`)
	if _, err := a.Open(context.Background(), ss[0]); err == nil {
		t.Fatal("replacement accepted")
	}
	ss, err = a.Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatal(ss, err)
	}
	if _, err := a.Open(context.Background(), ss[0]); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRootUsesPlatformDataDirectory(t *testing.T) {
	env := func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/data"
		}
		return ""
	}
	if got := defaultRoot("/home/u", "linux", env); got != filepath.Join("/data", "opencode") {
		t.Fatal(got)
	}
	env = func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Local`
		}
		return ""
	}
	if got := defaultRoot(`C:\Users\u`, "windows", env); got != filepath.Join(`C:\Local`, "opencode") {
		t.Fatal(got)
	}
}

func TestSnapshotMappingUsesBytesAlreadyRead(t *testing.T) {
	root := t.TempDir()
	write := func(p, b string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sp := filepath.Join(root, "storage/session/p/s.json")
	mp := filepath.Join(root, "storage/message/s/m.json")
	pp := filepath.Join(root, "storage/part/m/p.json")
	write(sp, `{"id":"s","directory":"/workspace/p"}`)
	write(mp, `{"id":"m","sessionID":"s","role":"user"}`)
	write(pp, `{"id":"p","sessionID":"s","messageID":"m","type":"text","text":"before"}`)
	a := New(root)
	a.afterSnapshotLoad = func() {
		a.afterSnapshotLoad = nil
		write(pp, `{"id":"p","sessionID":"s","messageID":"m","type":"text","text":"after"}`)
	}
	_, _, out, ok := a.inspect(context.Background(), sp)
	if !ok || !strings.Contains(string(out), "before") || strings.Contains(string(out), "after") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
}

func TestLoadSnapshotStopsBeforeRetainingFilesPastSessionLimit(t *testing.T) {
	root := t.TempDir()
	sp := filepath.Join(root, "storage/session/p/s.json")
	if err := os.MkdirAll(filepath.Dir(sp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte(`{"id":"s","directory":"/workspace/p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(root, "storage/message/s")
	if err := os.MkdirAll(md, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		body := fmt.Sprintf(`{"id":"m%02d","sessionID":"s","role":"user"}`, i)
		if err := os.WriteFile(filepath.Join(md, fmt.Sprintf("%02d.json", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(root)
	calls := 0
	a.readFile = func(r, p string) ([]byte, error) {
		calls++
		b, e := readBytes(r, p)
		if e == nil && strings.Contains(p, string(filepath.Separator)+"message"+string(filepath.Separator)) {
			b = append(b, bytes.Repeat([]byte(" "), 700<<10)...)
		}
		return b, e
	}
	snap, err := a.loadSnapshot(context.Background(), sp)
	if err == nil || calls >= 21 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if snap.files != nil || snap.total != 0 {
		t.Fatalf("oversize snapshot retained data: %#v", snap)
	}
}

func TestParseSnapshotVisitsEachFileOnce(t *testing.T) {
	s := snapshot{session: []byte(`{"id":"s","directory":"/workspace/p"}`), files: map[string][]byte{}}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("m%03d", i)
		s.files[filepath.Join("/root/storage/message/s", id+".json")] = []byte(fmt.Sprintf(`{"id":%q,"sessionID":"s","role":"user"}`, id))
		s.files[filepath.Join("/root/storage/part", id, "p.json")] = []byte(fmt.Sprintf(`{"id":"p","sessionID":"s","messageID":%q,"type":"text","text":"x"}`, id))
	}
	stats := &parseStats{}
	_, events, messages, bad, err := parseSnapshotWithStats(s, stats)
	if err != nil || bad != 0 || messages != 100 || len(events) != 100 {
		t.Fatalf("events=%d messages=%d bad=%d err=%v", len(events), messages, bad, err)
	}
	if stats.FileVisits != len(s.files) {
		t.Fatalf("visits=%d files=%d", stats.FileVisits, len(s.files))
	}
}

func TestParseSnapshotValidatesPhysicalPartParent(t *testing.T) {
	s := snapshot{session: []byte(`{"id":"s","directory":"/workspace/p"}`), files: map[string][]byte{
		filepath.Join("/root/storage/message/s", "m1.json"):         []byte(`{"id":"m1","sessionID":"s","role":"user"}`),
		filepath.Join("/root/storage/message/s", "m2.json"):         []byte(`{"id":"m2","sessionID":"s","role":"assistant"}`),
		filepath.Join("/root/storage/part/m1", "ok.json"):           []byte(`{"id":"ok","sessionID":"s","messageID":"m1","type":"text","text":"valid"}`),
		filepath.Join("/root/storage/part/m1", "lies.json"):         []byte(`{"id":"lies","sessionID":"s","messageID":"m2","type":"text","text":"must-not-move"}`),
		filepath.Join("/root/storage/part/missing", "unknown.json"): []byte(`{"id":"unknown","sessionID":"s","messageID":"missing","type":"text","text":"must-not-emit"}`),
	}}
	stats := &parseStats{}
	_, events, messages, bad, err := parseSnapshotWithStats(s, stats)
	if err != nil || messages != 2 || bad != 2 {
		t.Fatalf("messages=%d bad=%d err=%v", messages, bad, err)
	}
	if len(events) != 1 || events[0].Content.([]any)[0].(map[string]any)["text"] != "valid" {
		t.Fatalf("events=%#v", events)
	}
	if stats.FileVisits != len(s.files) {
		t.Fatalf("visits=%d files=%d", stats.FileVisits, len(s.files))
	}
}

func TestMixedMalformedCountMessageCountOrderingAndToolValues(t *testing.T) {
	root := t.TempDir()
	write := func(p, b string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "storage/session/p/s.json"), `{"id":"s","directory":"/workspace/p"}`)
	write(filepath.Join(root, "storage/message/s/u.json"), `{"id":"u","sessionID":"s","role":"user","time":{"created":1}}`)
	write(filepath.Join(root, "storage/message/s/a.json"), `{"id":"a","sessionID":"s","role":"assistant","time":{"created":2}}`)
	write(filepath.Join(root, "storage/part/u/good.json"), `{"id":"a","sessionID":"s","messageID":"u","type":"text","text":"user","time":{"created":1}}`)
	write(filepath.Join(root, "storage/part/u/empty.json"), `{"id":"b","sessionID":"s","messageID":"u","type":"text","text":" "}`)
	write(filepath.Join(root, "storage/part/a/reason.json"), `{"id":"a","sessionID":"s","messageID":"a","type":"reasoning","text":""}`)
	write(filepath.Join(root, "storage/part/a/toolbad.json"), `{"id":"b","sessionID":"s","messageID":"a","type":"tool","tool":"bash","state":{"input":{}}}`)
	write(filepath.Join(root, "storage/part/a/text.json"), `{"id":"c","sessionID":"s","messageID":"a","type":"text","text":"first","time":{"created":9}}`)
	write(filepath.Join(root, "storage/part/a/tool.json"), `{"id":"d","sessionID":"s","messageID":"a","type":"tool","tool":"bash","callID":"call-1","state":{"status":"completed","input":{"command":"go test"},"output":"PASS"},"time":{"created":9}}`)
	a := New(root)
	ss, err := a.Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%#v %v", ss, err)
	}
	if ss[0].MalformedCount != 3 || ss[0].MessageCount != 2 {
		t.Fatalf("session=%#v", ss[0])
	}
	r, err := a.Open(context.Background(), ss[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var ev []map[string]any
	d := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := d.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		ev = append(ev, e)
	}
	if len(ev) != 4 || ev[1]["content"].([]any)[0].(map[string]any)["text"] != "first" || ev[2]["type"] != "tool_use" || ev[3]["type"] != "tool_result" {
		t.Fatalf("events=%#v", ev)
	}
	if ev[2]["input"].(map[string]any)["command"] != "go test" || ev[3]["result"] != "PASS" {
		t.Fatalf("tool=%#v %#v", ev[2], ev[3])
	}
}

func TestDefaultRootPlatformEnvironmentMatrix(t *testing.T) {
	none := func(string) string { return "" }
	if got := defaultRoot("/home/u", "linux", none); got != filepath.Join("/home/u", ".local", "share", "opencode") {
		t.Fatal(got)
	}
	if got := defaultRoot("/Users/u", "darwin", none); got != filepath.Join("/Users/u", ".local", "share", "opencode") {
		t.Fatal(got)
	}
	xdg := func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/xdg"
		}
		return ""
	}
	if got := defaultRoot("/home/u", "linux", xdg); got != filepath.Join("/xdg", "opencode") {
		t.Fatal(got)
	}
	local := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Local`
		}
		return ""
	}
	if got := defaultRoot(`C:\Users\u`, "windows", local); got != filepath.Join(`C:\Local`, "opencode") {
		t.Fatal(got)
	}
}
