package upload

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestRedactYCSecrets(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"sk-proj-abcdefghijklmnopqrstuvwxyz",
		"AKIAABCDEFGHIJKLMNOP",
		"ghp_abcdefghijklmnopqrstuvwxyz",
		"eyJaaa.eyJbbb.signature",
		"postgres://alice:secret@db.example/app",
		"Bearer abcdefghijklmnopqrstuvwxyz",
		"Bearer: abcdefghijklmnopqrstuvwxyz",
		"OPENAI_API_KEY=secret-value-123456",
		"AIzaABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
		"sk_live_abcdefghijklmnopqrstuvwxyz",
		"xoxb-abcdefghijklmnopqrstuvwxyz",
		"xapp-abcdefghijklmnopqrstuvwxyz",
		"hf_abcdefghijklmnopqrstuvwxyz",
		"npm_abcdefghijklmnopqrstuvwxyz0123456789",
		"pypi-abcdefghijklmnopqrstuvwxyz",
		"yk_0123456789abcdef0123456789abcdef",
		"AC0123456789abcdef0123456789abcdef",
		"1//0abcdefghijklmnopqrstuvwxyz",
		"AccountKey=ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnop0123456789+/==",
		"v1.0-0123456789abcdef01234567-89abcdef0123456789abcdef",
		"cfat_abcdefghijklmnopqrstuvwxyz",
	}
	for _, secret := range secrets {
		t.Run(secret[:min(12, len(secret))], func(t *testing.T) {
			got, stats, err := RedactEvent(map[string]any{"text": secret})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got["text"].(string), secret) {
				t.Fatalf("secret leaked: %q", got["text"])
			}
			if stats.Replacements == 0 {
				t.Fatalf("no replacement recorded for %q", secret)
			}
		})
	}
}

func TestRedactYCHeaderAndOAuthAssignmentSecrets(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
		want   string
	}{
		{"auth key", "X-Auth-Key: abcdef0123456789", "abcdef0123456789", "X-Auth-Key: [REDACTED]"},
		{"auth email case whitespace", "x-auth-email \t: alice@example.edu", "alice@example.edu", "x-auth-email: [REDACTED]"},
		{"origin key", "X-Auth-User-Service-Key: v1.0-secret", "v1.0-secret", "X-Auth-User-Service-Key: [REDACTED]"},
		{"oauth quoted", `oauth_token = "opaque_token_123"`, "opaque_token_123", "oauth_token=[REDACTED]"},
		{"refresh colon", "REFRESH_TOKEN: refresh_12345", "refresh_12345", "REFRESH_TOKEN=[REDACTED]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, stats, err := RedactEvent(map[string]any{"text": test.input})
			if err != nil {
				t.Fatal(err)
			}
			text := got["text"].(string)
			if strings.Contains(text, test.secret) || text != test.want {
				t.Fatalf("secret leaked or structure changed: got=%q want=%q", text, test.want)
			}
			if stats.Replacements != 1 {
				t.Fatalf("stats=%+v", stats)
			}
			again, second, err := RedactEvent(got)
			if err != nil || !reflect.DeepEqual(got, again) || second != (Stats{}) {
				t.Fatalf("not idempotent: again=%#v stats=%+v err=%v", again, second, err)
			}
		})
	}
}

func TestRedactEventDropsForbiddenFieldsAcrossNamingVariants(t *testing.T) {
	input := map[string]any{
		"CwD": "/Users/alice/project", "session_id": "s1", "Parent-UUID": "p1",
		"authToken": "token", "Cookie": "sid=x", "phone_number": "13800138000",
		"nested":  []any{map[string]any{"e_mail": "alice@example.com", "filePath": "/tmp/x"}},
		"message": "keep semantic text",
	}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"CwD", "session_id", "Parent-UUID", "authToken", "Cookie", "phone_number", "e_mail", "filePath"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("forbidden field %q retained: %s", forbidden, encoded)
		}
	}
	if stats.DroppedFields != 8 || stats.RemovedFields != 8 {
		t.Fatalf("stats=%+v want 8 removed fields", stats)
	}
}

func TestRedactEventOmitsCorrelatedReadResultRecursively(t *testing.T) {
	input := map[string]any{"events": []any{
		map[string]any{"type": "tool_use", "call_id": "r1", "tool": map[string]any{"name": "Read"}},
		map[string]any{"type": "tool_result", "tool_call_id": "r1", "content": "COMPLETE SECRET FILE"},
		map[string]any{"type": "message", "content": "Read is a normal word"},
	}}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "COMPLETE SECRET FILE") {
		t.Fatalf("paired Read result leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), "Read is a normal word") {
		t.Fatalf("non-tool Read text was removed: %s", encoded)
	}
	if stats.OmittedReads != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestRedactEventCorrelatedReadResultUsesDeterministicShapesAndIDs(t *testing.T) {
	tests := []struct {
		name   string
		result map[string]any
		leaks  bool
	}{
		{
			name:   "type wins over conflicting role",
			result: map[string]any{"type": "tool_result", "role": "assistant", "tool_call_id": "read-1", "content": "SECRET"},
		},
		{
			name:   "role wins over conflicting type",
			result: map[string]any{"type": "message", "role": "tool_result", "tool_call_id": "read-1", "content": "SECRET"},
		},
		{
			name: "association id wins over own id",
			result: map[string]any{
				"type": "tool_result", "id": "unrelated-result-id", "tool_call_id": "read-1", "content": "SECRET",
			},
		},
		{
			name: "any explicit association id wins over own id",
			result: map[string]any{
				"type": "tool_result", "tool_call_id": "write-1", "tool_use_id": "read-1", "content": "SECRET",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := map[string]any{"events": []any{
				map[string]any{"type": "tool_use", "id": "read-1", "call_id": "ignored", "name": "Read"},
				test.result,
			}}
			for iteration := 0; iteration < 1000; iteration++ {
				got, _, err := RedactEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(got)
				leaked := strings.Contains(string(encoded), "SECRET")
				if leaked != test.leaks {
					t.Fatalf("iteration %d leaked=%v want %v: %s", iteration, leaked, test.leaks, encoded)
				}
			}
		})
	}
}

func TestRedactEventCorrelatedReadResultIsConcurrencySafe(t *testing.T) {
	event := map[string]any{"events": []any{
		map[string]any{"type": "tool_use", "id": "read-1", "name": "Read"},
		map[string]any{"type": "tool_result", "role": "assistant", "id": "result-1", "tool_use_id": "read-1", "content": "SECRET"},
	}}
	var wait sync.WaitGroup
	errs := make(chan string, 32)
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1000; iteration++ {
				got, _, err := RedactEvent(event)
				encoded, _ := json.Marshal(got)
				if err != nil || strings.Contains(string(encoded), "SECRET") {
					errs <- string(encoded)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errs)
	for leaked := range errs {
		t.Fatalf("correlated Read result leaked: %s", leaked)
	}
}

func TestRedactEventIsIdempotent(t *testing.T) {
	once, firstStats, err := RedactEvent(map[string]any{"text": "alice@example.com /Users/alice/a sk-proj-abcdefghijklmnopqrstuvwxyz"})
	if err != nil {
		t.Fatal(err)
	}
	twice, secondStats, err := RedactEvent(once)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("not idempotent:\nonce=%#v\ntwice=%#v", once, twice)
	}
	if firstStats.Replacements != 3 || secondStats.Replacements != 0 {
		t.Fatalf("stats first=%+v second=%+v", firstStats, secondStats)
	}
}

func TestRedactEventRejectsUnsafeValuesAndLimits(t *testing.T) {
	tests := map[string]map[string]any{
		"NaN":            {"v": math.NaN()},
		"non-string map": {"v": map[any]any{1: "secret"}},
		"custom type":    {"v": struct{ Secret string }{"secret"}},
		"large string":   {"v": strings.Repeat("x", maxRedactionStringBytes+1)},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := RedactEvent(input); err == nil {
				t.Fatal("expected fail-closed error")
			}
		})
	}

	tooMany := make([]any, maxRedactionNodes)
	if _, _, err := RedactEvent(map[string]any{"items": tooMany}); err == nil {
		t.Fatal("expected node limit error")
	}
}

func TestRedactEventURLQuerySecretsAndStablePlaceholders(t *testing.T) {
	secret := "alice@example.edu"
	input := map[string]any{"text": "mail " + secret + " twice " + secret +
		" https://example.test/api?token=top-secret&view=public"}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	text := got["text"].(string)
	if strings.Contains(text, secret) || strings.Contains(text, "top-secret") {
		t.Fatalf("PII or query secret leaked: %q", text)
	}
	if strings.Count(text, "[REDACTED_EMAIL]") != 2 ||
		!strings.Contains(text, "token=[REDACTED]&view=public") {
		t.Fatalf("unstable or structurally damaging redaction: %q", text)
	}
	if stats.Replacements != 3 {
		t.Fatalf("stats=%+v", stats)
	}
}

func FuzzRedactEventNeverMutatesAndIsIdempotent(f *testing.F) {
	for _, seed := range []string{
		"plain text", "alice@example.com", "13800138000", "/Users/张三/project",
		`C:\Users\Alice\project`, "Bearer abcdefghijklmnopqrstuvwxyz",
		"https://example.test/x?api_key=secret-value",
		"/0000000 000000000",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 || !utf8.ValidString(value) {
			t.Skip()
		}
		input := map[string]any{"nested": []any{map[string]any{"text": value}}}
		before, _ := json.Marshal(input)
		once, _, err := RedactEvent(input)
		if err != nil {
			t.Fatalf("safe JSON value rejected: %v", err)
		}
		after, _ := json.Marshal(input)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("input mutated")
		}
		twice, stats, err := RedactEvent(once)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(once, twice) || stats != (Stats{}) {
			t.Fatalf("not idempotent: once=%#v twice=%#v stats=%+v", once, twice, stats)
		}
	})
}

func FuzzReadCorrelationMapShapes(f *testing.F) {
	f.Add("tool_result", "assistant", "read-1", "result-1")
	f.Add("message", "tool_result", "read-1", "read-1")
	f.Fuzz(func(t *testing.T, eventType, role, associationID, ownID string) {
		if len(eventType)+len(role)+len(associationID)+len(ownID) > 1024 ||
			!utf8.ValidString(eventType) || !utf8.ValidString(role) ||
			!utf8.ValidString(associationID) || !utf8.ValidString(ownID) {
			t.Skip()
		}
		event := map[string]any{"events": []any{
			map[string]any{"id": "read-1", "name": "Read"},
			map[string]any{
				"type": eventType, "role": role, "id": ownID,
				"tool_call_id": associationID, "content": "SECRET",
			},
		}}
		got, _, err := RedactEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(got)
		isResult := normalizeName(eventType) == "toolresult" || normalizeName(eventType) == "toolresponse" ||
			normalizeName(role) == "toolresult" || normalizeName(role) == "toolresponse"
		shouldOmit := isResult && associationID == "read-1"
		if strings.Contains(string(encoded), "SECRET") == shouldOmit {
			t.Fatalf("correlation mismatch: %s", encoded)
		}
	})
}

func TestRedactEventDeepCopiesDropsAndRedacts(t *testing.T) {
	input := map[string]any{
		"attachment": map[string]any{"file_content": "TOP SECRET FILE"},
		"nested": []any{map[string]any{
			"email": "alice@example.com",
			"data":  "Bearer abcdefghijklmnop sk-abcdefghijklmnopqrstuvwxyz 13812345678 10.1.2.3 /Users/alice/private.txt C:\\Users\\alice\\secret.txt",
		}},
		"diff": "@@ -1 +1 @@\n-old\n+new",
	}
	before, _ := json.Marshal(input)

	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatalf("RedactEvent: %v", err)
	}
	after, _ := json.Marshal(input)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("RedactEvent mutated input")
	}
	encoded, _ := json.Marshal(got)
	text := string(encoded)
	for _, secret := range []string{"TOP SECRET FILE", "alice@example.com", "abcdefghijklmnop", "sk-abcdefghijklmnopqrstuvwxyz", "13812345678", "10.1.2.3", "/Users/alice/private.txt", `C:\\Users\\alice\\secret.txt`} {
		if strings.Contains(text, secret) {
			t.Errorf("output contains secret %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "@@ -1 +1 @@") {
		t.Errorf("diff removed: %s", text)
	}
	if stats.DroppedFields != 2 || stats.RemovedFields != 2 || stats.Replacements < 6 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRedactEventOmitsReadOutputButPreservesPatch(t *testing.T) {
	read := map[string]any{"tool_name": "read_file", "output": "complete file", "result": []any{"also secret"}}
	got, stats, err := RedactEvent(read)
	if err != nil {
		t.Fatal(err)
	}
	if got["output"] != "[OMITTED_FILE_CONTENT]" || got["result"] != "[OMITTED_FILE_CONTENT]" {
		t.Fatalf("read output not omitted: %#v", got)
	}
	if stats.OmittedReads != 2 {
		t.Fatalf("OmittedReads = %d, want 2", stats.OmittedReads)
	}

	patch, _, err := RedactEvent(map[string]any{"name": "apply_patch", "output": "diff --git a/a b/a"})
	if err != nil || patch["output"] != "diff --git a/a b/a" {
		t.Fatalf("patch output changed: %#v, %v", patch, err)
	}
}

func TestRedactEventPrivateKeyAndURL(t *testing.T) {
	input := map[string]any{"value": "-----BEGIN PRIVATE KEY-----\nabc123\n-----END PRIVATE KEY----- visit https://example.com/a/b"}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["value"].(string)
	if strings.Contains(value, "abc123") || !strings.Contains(value, "[REDACTED_PRIVATE_KEY]") {
		t.Fatalf("private key not redacted: %q", value)
	}
	if !strings.Contains(value, "https://example.com/a/b") {
		t.Fatalf("URL damaged: %q", value)
	}
	if stats.Replacements != 1 {
		t.Fatalf("Replacements = %d, want 1", stats.Replacements)
	}
}

func TestRedactEventDepthLimit(t *testing.T) {
	var value any = "leaf"
	for i := 0; i < 65; i++ {
		value = map[string]any{"child": value}
	}
	if _, _, err := RedactEvent(value.(map[string]any)); err == nil {
		t.Fatal("expected depth error")
	}
}

func TestRedactEventTokenLabelsAndShortSK(t *testing.T) {
	input := map[string]any{"v": "api_key=abcdefghijklmnop apikey: qrstuvwxyzABCDEF token=1234567890abcdef secret=FEDCBA0987654321 sk-123456789012 ordinarylongnaturallanguagevalue short=abc"}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	for _, secret := range []string{"abcdefghijklmnop", "qrstuvwxyzABCDEF", "1234567890abcdef", "FEDCBA0987654321", "sk-123456789012"} {
		if strings.Contains(value, secret) {
			t.Errorf("secret remains %q in %q", secret, value)
		}
	}
	if !strings.Contains(value, "ordinarylongnaturallanguagevalue") || !strings.Contains(value, "short=abc") {
		t.Fatalf("ordinary text damaged: %q", value)
	}
	if stats.Replacements != 5 {
		t.Fatalf("Replacements=%d want 5", stats.Replacements)
	}
}

func TestRedactEventInternationalPhones(t *testing.T) {
	got, stats, err := RedactEvent(map[string]any{"v": "call +1-415-555-2671 or (020) 7946 0958; date 2026-07-28 short 12345"})
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	if strings.Contains(value, "415-555") || strings.Contains(value, "7946 0958") {
		t.Fatalf("phone remains: %q", value)
	}
	if !strings.Contains(value, "2026-07-28") || !strings.Contains(value, "12345") {
		t.Fatalf("non-phone damaged: %q", value)
	}
	if stats.Replacements != 2 {
		t.Fatalf("Replacements=%d want 2", stats.Replacements)
	}
}

func TestRedactEventUnicodeAndQuotedPaths(t *testing.T) {
	input := map[string]any{"v": `"/Users/张三/My Project/秘密.txt" /用户/张三/文件.txt "C:\Users\张三\My Project\秘密.txt" / C:\ https://example.com/a/b`}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	for _, path := range []string{"/Users/张三/My Project/秘密.txt", "/用户/张三/文件.txt", `C:\Users\张三\My Project\秘密.txt`, `C:\`} {
		if strings.Contains(value, path) {
			t.Errorf("path remains %q in %q", path, value)
		}
	}
	if !strings.Contains(value, "https://example.com/a/b") {
		t.Fatalf("URL damaged: %q", value)
	}
	if stats.Replacements != 5 {
		t.Fatalf("Replacements=%d want 5 (%q)", stats.Replacements, value)
	}
}

func TestRedactEventExactFieldStats(t *testing.T) {
	got, stats, err := RedactEvent(map[string]any{"attachments": "x", "file_content": "y", "tool-name": "cat", "output": "z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["attachments"]; ok {
		t.Fatal("attachments retained")
	}
	if _, ok := got["file_content"]; ok {
		t.Fatal("file_content retained")
	}
	if stats != (Stats{RemovedFields: 2, DroppedFields: 2, OmittedReads: 1}) {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestRedactEventStructuredCredentialFields(t *testing.T) {
	input := map[string]any{
		"api_key": "abcdefghijklmnop",
		"Token":   "1234567890abcdef",
		"nested": []any{
			map[string]any{"access-token": "sk-123456789012"},
			map[string]any{"client_secret": "ABCDEFGHIJKLMNOP"},
		},
		"short": map[string]any{"secret": "ordinary"},
	}
	before, _ := json.Marshal(input)
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("input mutated")
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"abcdefghijklmnop", "1234567890abcdef", "sk-123456789012", "ABCDEFGHIJKLMNOP"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("structured secret remains %q: %s", secret, encoded)
		}
	}
	if strings.Contains(string(encoded), "ordinary") {
		t.Fatalf("short structured secret remains: %s", encoded)
	}
	if stats.Replacements != 0 || stats.RemovedFields != 5 || stats.DroppedFields != 5 {
		t.Fatalf("stats=%+v want 5 removed fields", stats)
	}
}

func TestRedactEventFailClosedCredentialKeysAndWindowsPaths(t *testing.T) {
	input := map[string]any{
		"password":              "a b!",
		"passwd":                "x",
		"pwd":                   1234,
		"db_password":           "short",
		"databasePassword":      "contains spaces",
		"aws_secret_access_key": "special!@#",
		"secretAccessKey":       "ordinary",
		"private_key":           "not PEM",
		"client-secret":         "tiny",
		"message":               `open \\server\share\private file.txt then C:/Users/alice/secret.txt`,
	}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"a b!", `"passwd":"x"`, `"pwd":1234`, "contains spaces", "special!@#", "ordinary", "not PEM", "tiny", `server\\share`, "C:/Users/alice"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("secret/path remains %q: %s", secret, encoded)
		}
	}
	if stats.Replacements != 2 || stats.RemovedFields != 9 || stats.DroppedFields != 9 {
		t.Fatalf("stats=%+v want 2 replacements and 9 removed fields", stats)
	}
}

func TestRedactEventPathBoundariesAndUnquotedSpaces(t *testing.T) {
	input := map[string]any{"v": "[/tmp/secret];/tmp/second (/用户/项目 文件/main.go),C:\\Users\\张三\\My Project\\main.go; https://example.com/a/b"}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	for _, path := range []string{"/tmp/secret", "/tmp/second", "/用户/项目 文件/main.go", `C:\Users\张三\My Project\main.go`} {
		if strings.Contains(value, path) {
			t.Errorf("path remains %q in %q", path, value)
		}
	}
	if !strings.Contains(value, "[[REDACTED_PATH]]") || !strings.Contains(value, ";[REDACTED_PATH]") || !strings.Contains(value, "([REDACTED_PATH])") {
		t.Fatalf("surrounding punctuation lost: %q", value)
	}
	if !strings.Contains(value, "https://example.com/a/b") {
		t.Fatalf("URL damaged: %q", value)
	}
	if stats.Replacements != 4 {
		t.Fatalf("Replacements=%d want 4 (%q)", stats.Replacements, value)
	}
}

func TestRedactEventPathsAfterLineBreaks(t *testing.T) {
	input := map[string]any{"v": "log\n/tmp/secret\nnext\r\nC:\\secret.txt\r\n(/用户/项目 文件/main.go)\nsafe"}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	for _, path := range []string{"/tmp/secret", `C:\secret.txt`, "/用户/项目 文件/main.go"} {
		if strings.Contains(value, path) {
			t.Errorf("path remains %q in %q", path, value)
		}
	}
	if !strings.Contains(value, "log\n[REDACTED_PATH]\nnext\r\n[REDACTED_PATH]\r\n([REDACTED_PATH])\nsafe") {
		t.Fatalf("line structure damaged: %q", value)
	}
	if stats.Replacements != 3 {
		t.Fatalf("Replacements=%d want 3", stats.Replacements)
	}
}

func TestRedactEventPathContexts(t *testing.T) {
	input := map[string]any{"v": `open /tmp/a then "/tmp/a,b;c file" ([/用户/项目 文件,a;b.go]) /tmp/secret,backup.txt https://example.com/a,b`}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	if value != `open [REDACTED_PATH] then "[REDACTED_PATH]" ([[REDACTED_PATH]]) [REDACTED_PATH] https://example.com/a,b` {
		t.Fatalf("redacted=%q", value)
	}
	if stats.Replacements != 4 {
		t.Fatalf("Replacements=%d want 4", stats.Replacements)
	}
}

func TestRedactEventPhoneUTF8Suffixes(t *testing.T) {
	got, stats, err := RedactEvent(map[string]any{"v": "13812345678中 13812345679🙂 13812345670)"})
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	if !utf8.ValidString(value) {
		t.Fatalf("invalid UTF-8: %q", value)
	}
	if value != "[REDACTED_PHONE]中 [REDACTED_PHONE]🙂 [REDACTED_PHONE])" {
		t.Fatalf("redacted=%q", value)
	}
	if stats.Replacements != 3 {
		t.Fatalf("Replacements=%d want 3", stats.Replacements)
	}
}

func TestRedactEventUnquotedPathsWithLimitedContinuation(t *testing.T) {
	input := map[string]any{"v": `/Users/alice/My Project/secret.txt /用户/项目 文件/main.go C:\Users\Alice\My Project\secret.txt open /tmp/a then continue`}
	got, stats, err := RedactEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	value := got["v"].(string)
	want := `[REDACTED_PATH] [REDACTED_PATH] [REDACTED_PATH] open [REDACTED_PATH] then continue`
	if value != want {
		t.Fatalf("redacted=%q want %q", value, want)
	}
	if stats.Replacements != 4 {
		t.Fatalf("Replacements=%d want 4 (%q)", stats.Replacements, value)
	}
}
