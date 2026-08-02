package upload

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const omittedFileContent = "[OMITTED_FILE_CONTENT]"
const (
	maxRedactionDepth       = 64
	maxRedactionNodes       = 100_000
	maxRedactionStringBytes = 1 << 20
)

var (
	privateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN(?: [A-Z0-9]+)? PRIVATE KEY-----.*?-----END(?: [A-Z0-9]+)? PRIVATE KEY-----`)
	bearerPattern           = regexp.MustCompile(`(?i)\bBearer[ \t:=]+[A-Za-z0-9._~+/=-]+`)
	apiKeyPattern           = regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{12,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{16,}|AKIA[A-Z0-9]{16})\b`)
	anthropicKeyPattern     = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)
	openAIKeyPattern        = regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`)
	githubTokenPattern      = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}`)
	googleAPIKeyPattern     = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}\b`)
	jwtPattern              = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	dbURLPattern            = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|redis|mongodb(?:\+srv)?|mysql|mssql|amqps?)://[^\s:@/]*:[^\s@]+@\S+`)
	stripePattern           = regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{20,}`)
	slackPattern            = regexp.MustCompile(`\b(?:xox[baprs]|xapp)-[A-Za-z0-9-]{10,}`)
	huggingFacePattern      = regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}`)
	npmPattern              = regexp.MustCompile(`\bnpm_[A-Za-z0-9]{30,}`)
	pypiPattern             = regexp.MustCompile(`\bpypi-[A-Za-z0-9_-]{20,}`)
	ycTokenPattern          = regexp.MustCompile(`\byk_[0-9a-f]{16,}`)
	twilioPattern           = regexp.MustCompile(`\b(?:AC|SK)[0-9a-f]{32}\b`)
	googleOAuthPattern      = regexp.MustCompile(`\b1//0[A-Za-z0-9_-]{20,}`)
	azureKeyPattern         = regexp.MustCompile(`\bAccountKey=[A-Za-z0-9+/=]{40,}`)
	cloudflarePattern       = regexp.MustCompile(`\b(?:v1\.0-[0-9a-f]{24,}-[0-9a-f]{24,}|cf(?:oat|at|ut|k)_[A-Za-z0-9]{20,})`)
	cloudflareHeaderPattern = regexp.MustCompile(`(?i)\b(X-Auth-(?:Key|Email|User-Service-Key))\s*:\s*[^\s"',;]+`)
	oauthAssignmentPattern  = regexp.MustCompile(`(?i)\b(oauth_token|refresh_token)\b\s*[:=]\s*["']?[A-Za-z0-9_~+/=-]{8,}["']?`)
	envSecretPattern        = regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|KEY)S?)\s*=\s*[^\s"'\x60\n,;.]+`)
	querySecretPattern      = regexp.MustCompile(`(?i)([?&](?:access_token|auth|api[_-]?key|token|secret|password|ticket)=)[^&#\s]+`)
	labeledTokenPattern     = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|token|secret)([ \t]*[:=][ \t]*)([A-Za-z0-9._~+/=-]{16,})`)
	emailPattern            = regexp.MustCompile(`\b[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b`)
	phoneCandidatePattern   = regexp.MustCompile(`\+?(?:\([0-9]{1,4}\)|[0-9])[0-9() -]{5,}[0-9]([^0-9.]|\.(?:[^0-9]|$)|$)`)
	datePattern             = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	ipv4Pattern             = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
)

func RedactEvent(event map[string]any) (map[string]any, Stats, error) {
	if event == nil {
		return nil, Stats{}, nil
	}
	state := redactionState{readIDs: make(map[string]struct{})}
	if err := collectReadIDs(event, 0, &state); err != nil {
		return nil, Stats{}, err
	}
	state.nodes = 0
	value, stats, err := redactValue(event, 0, &state)
	if err != nil {
		return nil, Stats{}, err
	}
	return value.(map[string]any), stats, nil
}

type redactionState struct {
	nodes   int
	readIDs map[string]struct{}
}

func (s *redactionState) visit(depth int) error {
	if depth > maxRedactionDepth {
		return errors.New("event nesting limit exceeded")
	}
	s.nodes++
	if s.nodes > maxRedactionNodes {
		return errors.New("event node limit exceeded")
	}
	return nil
}

func redactValue(value any, depth int, state *redactionState) (any, Stats, error) {
	if err := state.visit(depth); err != nil {
		return nil, Stats{}, err
	}
	switch typed := value.(type) {
	case map[string]any:
		return redactMap(typed, depth, state)
	case []any:
		out := make([]any, len(typed))
		var total Stats
		for i, item := range typed {
			copied, stats, err := redactValue(item, depth+1, state)
			if err != nil {
				return nil, Stats{}, err
			}
			out[i] = copied
			addStats(&total, stats)
		}
		return out, total, nil
	case string:
		if len(typed) > maxRedactionStringBytes {
			return nil, Stats{}, errors.New("event string limit exceeded")
		}
		if !utf8.ValidString(typed) {
			return nil, Stats{}, errors.New("event contains invalid UTF-8")
		}
		redacted, replacements := redactString(typed)
		return redacted, Stats{Replacements: replacements}, nil
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		if number, ok := numericFloat(typed); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
			return nil, Stats{}, errors.New("event contains non-finite number")
		}
		return typed, Stats{}, nil
	default:
		return nil, Stats{}, fmt.Errorf("unsupported event value type %T", value)
	}
}

func numericFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	default:
		return 0, false
	}
}

func redactMap(input map[string]any, depth int, state *redactionState) (map[string]any, Stats, error) {
	out := make(map[string]any, len(input))
	var total Stats
	read := isReadTool(input)
	pairedReadResult := isCorrelatedReadResult(input, state.readIDs)
	for key, value := range input {
		if isDroppedKey(key) || isForbiddenKey(key) || isCredentialKey(key) {
			total.DroppedFields++
			total.RemovedFields++
			continue
		}
		if (read || pairedReadResult) && isReadPayloadKey(key) {
			out[key] = omittedFileContent
			if value != omittedFileContent {
				total.OmittedReads++
			}
			continue
		}
		copied, stats, err := redactValue(value, depth+1, state)
		if err != nil {
			return nil, Stats{}, err
		}
		out[key] = copied
		addStats(&total, stats)
	}
	return out, total, nil
}

func isCredentialKey(key string) bool {
	switch normalizeName(key) {
	case "apikey", "accesstoken", "authtoken", "refreshtoken", "token", "secret", "clientsecret",
		"password", "passwd", "pwd", "dbpassword", "databasepassword",
		"dbpass", "awssecretaccesskey", "secretaccesskey", "privatekey",
		"passphrase", "credential", "credentials", "connectionstring":
		return true
	default:
		return false
	}
}

func normalizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Map(func(char rune) rune {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			return char
		}
		return -1
	}, value)
}

func isForbiddenKey(key string) bool {
	switch normalizeName(key) {
	case "cwd", "path", "filepath", "fullpath", "absolutepath", "sessionid",
		"uuid", "parentuuid", "token", "accesstoken", "authtoken", "refreshtoken",
		"authorization", "auth", "cookie", "cookies", "setcookie", "phone",
		"phonenumber", "mobile", "email", "emailaddress", "otp", "password",
		"passwd", "pwd", "apikey", "clientsecret", "secret", "credentials",
		"credential", "privatekey", "connectionstring", "ticket":
		return true
	default:
		return false
	}
}

func isDroppedKey(key string) bool {
	switch normalizeName(key) {
	case "attachment", "attachments", "image", "images", "binary", "blob", "filecontent", "filecontents":
		return true
	default:
		return false
	}
}

func isReadPayloadKey(key string) bool {
	switch normalizeName(key) {
	case "content", "output", "result":
		return true
	default:
		return false
	}
}

func isReadTool(input map[string]any) bool {
	for key, value := range input {
		normalizedKey := normalizeName(key)
		if normalizedKey != "toolname" && normalizedKey != "name" {
			continue
		}
		name, ok := value.(string)
		if !ok {
			continue
		}
		switch normalizeName(name) {
		case "read", "readfile", "cat":
			return true
		}
	}
	for key, value := range input {
		if normalizeName(key) != "tool" {
			continue
		}
		if nested, ok := value.(map[string]any); ok && isReadTool(nested) {
			return true
		}
	}
	return false
}

func collectReadIDs(value any, depth int, state *redactionState) error {
	if err := state.visit(depth); err != nil {
		return err
	}
	switch typed := value.(type) {
	case map[string]any:
		if isReadTool(typed) {
			if id := readCallID(typed); id != "" {
				state.readIDs[id] = struct{}{}
			}
		}
		for _, child := range typed {
			if err := collectReadIDs(child, depth+1, state); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := collectReadIDs(child, depth+1, state); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > maxRedactionStringBytes || !utf8.ValidString(typed) {
			return errors.New("event contains invalid or oversized string")
		}
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		if number, ok := numericFloat(typed); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
			return errors.New("event contains non-finite number")
		}
	default:
		return fmt.Errorf("unsupported event value type %T", value)
	}
	return nil
}

func readCallID(input map[string]any) string {
	return prioritizedStringField(input, "id", "callid", "tooluseid", "toolcallid")
}

func resultAssociationIDs(input map[string]any) []string {
	ids := make([]string, 0, 3)
	for _, field := range []string{"toolcallid", "tooluseid", "callid"} {
		if id := prioritizedStringField(input, field); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) != 0 {
		return ids
	}
	if id := prioritizedStringField(input, "id"); id != "" {
		return []string{id}
	}
	return nil
}

func prioritizedStringField(input map[string]any, normalizedNames ...string) string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, wanted := range normalizedNames {
		for _, key := range keys {
			if normalizeName(key) != wanted {
				continue
			}
			if value, ok := input[key].(string); ok {
				return value
			}
		}
	}
	return ""
}

func isCorrelatedReadResult(input map[string]any, ids map[string]struct{}) bool {
	resultShape := false
	for _, field := range []string{"type", "role"} {
		if name := prioritizedStringField(input, field); name != "" {
			normalized := normalizeName(name)
			resultShape = resultShape || normalized == "toolresult" || normalized == "toolresponse"
		}
	}
	if !resultShape {
		return false
	}
	for _, id := range resultAssociationIDs(input) {
		if _, exists := ids[id]; exists {
			return true
		}
	}
	return false
}

func redactString(input string) (string, int) {
	out := input
	count := 0
	replace := func(pattern *regexp.Regexp, replacement string) {
		out = pattern.ReplaceAllStringFunc(out, func(string) string {
			count++
			return replacement
		})
	}
	replace(privateKeyPattern, "[REDACTED_PRIVATE_KEY]")
	replace(anthropicKeyPattern, "[REDACTED_ANTHROPIC_KEY]")
	replace(openAIKeyPattern, "[REDACTED_OPENAI_KEY]")
	replace(githubTokenPattern, "[REDACTED_GITHUB_TOKEN]")
	replace(googleAPIKeyPattern, "[REDACTED_GOOGLE_API_KEY]")
	replace(jwtPattern, "[REDACTED_JWT]")
	replace(bearerPattern, "[REDACTED_TOKEN]")
	replace(dbURLPattern, "[REDACTED_DATABASE_URL]")
	replace(stripePattern, "[REDACTED_STRIPE_KEY]")
	replace(slackPattern, "[REDACTED_SLACK_TOKEN]")
	replace(huggingFacePattern, "[REDACTED_HF_TOKEN]")
	replace(npmPattern, "[REDACTED_NPM_TOKEN]")
	replace(pypiPattern, "[REDACTED_PYPI_TOKEN]")
	replace(ycTokenPattern, "[REDACTED_YC_TOKEN]")
	replace(twilioPattern, "[REDACTED_TWILIO_KEY]")
	replace(googleOAuthPattern, "[REDACTED_GOOGLE_OAUTH]")
	replace(azureKeyPattern, "[REDACTED_AZURE_KEY]")
	replace(cloudflarePattern, "[REDACTED_CLOUDFLARE_TOKEN]")
	out = cloudflareHeaderPattern.ReplaceAllStringFunc(out, func(match string) string {
		if strings.Contains(match, "[REDACTED") {
			return match
		}
		separator := strings.IndexByte(match, ':')
		if separator < 0 {
			return match
		}
		count++
		return strings.TrimSpace(match[:separator]) + ": [REDACTED]"
	})
	out = oauthAssignmentPattern.ReplaceAllStringFunc(out, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		if separator < 0 {
			return match
		}
		count++
		return strings.TrimSpace(match[:separator]) + "=[REDACTED]"
	})
	out = querySecretPattern.ReplaceAllStringFunc(out, func(match string) string {
		separator := strings.LastIndexByte(match, '=')
		if separator < 0 || strings.HasPrefix(match[separator+1:], "[REDACTED") {
			return match
		}
		count++
		return match[:separator+1] + "[REDACTED]"
	})
	out = envSecretPattern.ReplaceAllStringFunc(out, func(match string) string {
		separator := strings.IndexByte(match, '=')
		if separator < 0 || strings.HasPrefix(strings.TrimSpace(match[separator+1:]), "[REDACTED") {
			return match
		}
		count++
		return strings.TrimSpace(match[:separator]) + "=[REDACTED]"
	})
	labeledMatches := len(labeledTokenPattern.FindAllStringIndex(out, -1))
	out = labeledTokenPattern.ReplaceAllString(out, `$1$2[REDACTED_TOKEN]`)
	count += labeledMatches
	replace(apiKeyPattern, "[REDACTED_TOKEN]")
	replace(emailPattern, "[REDACTED_EMAIL]")
	var paths int
	out, paths = redactPaths(out)
	count += paths
	out = phoneCandidatePattern.ReplaceAllStringFunc(out, func(match string) string {
		parts := phoneCandidatePattern.FindStringSubmatchIndex(match)
		candidate := match[:parts[2]]
		suffix := match[parts[2]:parts[3]]
		digits := 0
		for _, char := range candidate {
			if char >= '0' && char <= '9' {
				digits++
			}
		}
		if digits < 7 || digits > 15 || datePattern.MatchString(candidate) {
			return match
		}
		count++
		return "[REDACTED_PHONE]" + suffix
	})
	replace(ipv4Pattern, "[REDACTED_IP]")
	return out, count
}

func redactPaths(input string) (string, int) {
	var out strings.Builder
	count, last := 0, 0
	for i := 0; i < len(input); {
		_, prefixLen := absolutePathStart(input, i)
		if prefixLen == 0 || !pathBoundary(input, i) {
			i++
			continue
		}
		end := i + prefixLen
		close := byte(0)
		if i > 0 {
			switch input[i-1] {
			case '"', '\'':
				close = input[i-1]
			case '(':
				close = ')'
			case '[':
				close = ']'
			case '{':
				close = '}'
			}
		}
		for end < len(input) && input[end] != '\r' && input[end] != '\n' {
			if close != 0 {
				if input[end] == close {
					break
				}
			} else if isPathSpace(input[end]) {
				if continued, ok := unquotedPathContinuation(input, end); ok {
					end = continued
				}
				break
			}
			end++
		}
		out.WriteString(input[last:i])
		out.WriteString("[REDACTED_PATH]")
		count++
		last, i = end, end
	}
	out.WriteString(input[last:])
	return out.String(), count
}

func unquotedPathContinuation(input string, whitespace int) (int, bool) {
	pos := whitespace
	for tokenNumber := 0; tokenNumber < 3; tokenNumber++ {
		for pos < len(input) && isPathSpace(input[pos]) {
			pos++
		}
		if pos >= len(input) || input[pos] == '\r' || input[pos] == '\n' || isPairedClose(input[pos]) {
			return 0, false
		}
		switch input[pos] {
		case '(', '[', '{', '"', '\'':
			return 0, false
		}
		if _, length := absolutePathStart(input, pos); length != 0 {
			return 0, false
		}
		start := pos
		for pos < len(input) && !isPathSpace(input[pos]) && input[pos] != '\r' && input[pos] != '\n' && !isPairedClose(input[pos]) {
			pos++
		}
		token := input[start:pos]
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			return 0, false
		}
		if strings.ContainsAny(token, `/\`) || hasFileExtension(token) {
			return pos, true
		}
	}
	return 0, false
}

func hasFileExtension(token string) bool {
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 || len(token)-dot-1 < 1 || len(token)-dot-1 > 10 {
		return false
	}
	for _, char := range token[dot+1:] {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}

func isPairedClose(char byte) bool {
	switch char {
	case ')', ']', '}', '"', '\'':
		return true
	default:
		return false
	}
}

func absolutePathStart(input string, i int) (int, int) {
	if input[i] == '\\' && i+1 < len(input) && input[i+1] == '\\' {
		return 2, 2
	}
	if input[i] == '/' {
		if i+1 < len(input) && input[i+1] == '/' {
			return 0, 0
		}
		return 1, 1
	}
	if i+2 < len(input) && ((input[i] >= 'A' && input[i] <= 'Z') || (input[i] >= 'a' && input[i] <= 'z')) &&
		input[i+1] == ':' && (input[i+2] == '\\' || input[i+2] == '/') {
		return 2, 3
	}
	return 0, 0
}

func pathBoundary(input string, i int) bool {
	if i == 0 {
		return true
	}
	switch input[i-1] {
	case ' ', '\t', '\r', '\n', '[', '(', '{', '=', ',', ':', ';', '"', '\'':
		return true
	default:
		return false
	}
}

func isPathSpace(char byte) bool { return char == ' ' || char == '\t' }

func addStats(total *Stats, next Stats) {
	total.Replacements += next.Replacements
	total.RemovedFields += next.RemovedFields
	total.DroppedFields += next.DroppedFields
	total.OmittedReads += next.OmittedReads
}
