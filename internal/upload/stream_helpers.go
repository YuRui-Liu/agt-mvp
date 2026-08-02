package upload

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"unicode/utf8"
)

const (
	defaultMaxLineBytes    = int64(1 << 20)
	defaultMaxSessionBytes = int64(4 << 20)
	defaultMaxPackageBytes = int64(25 << 20)
)

func withDefaultLimits(limits Limits) Limits {
	if limits.MaxLineBytes <= 0 {
		limits.MaxLineBytes = defaultMaxLineBytes
	}
	if limits.MaxSessionBytes <= 0 {
		limits.MaxSessionBytes = defaultMaxSessionBytes
	}
	if limits.MaxPackageBytes <= 0 {
		limits.MaxPackageBytes = defaultMaxPackageBytes
	}
	return limits
}

func scanEvents(reader io.Reader, limits Limits, correlationNamespace string) ([]map[string]any, Stats, error) {
	sessionReadLimit := limits.MaxSessionBytes
	if sessionReadLimit < math.MaxInt64 {
		sessionReadLimit++
	}
	limited := &io.LimitedReader{R: reader, N: sessionReadLimit}
	counted := &countingReader{reader: limited}
	scanner := bufio.NewScanner(counted)
	initial := 64 * 1024
	scannerMax := limits.MaxLineBytes + 1
	maxInt := int64(^uint(0) >> 1)
	if scannerMax <= 0 || scannerMax > maxInt {
		scannerMax = maxInt
	}
	if scannerMax < int64(initial) {
		initial = int(scannerMax)
	}
	scanner.Buffer(make([]byte, initial), int(scannerMax))
	lineTooLong := false
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		advance, token, err = bufio.ScanLines(data, atEOF)
		if token != nil && int64(len(token)) > limits.MaxLineBytes {
			lineTooLong = true
			return 0, nil, errors.New("line limit")
		}
		if token == nil && int64(len(data)) > limits.MaxLineBytes {
			lineTooLong = true
			return 0, nil, errors.New("line limit")
		}
		return advance, token, err
	})
	var (
		rawEvents []map[string]any
	)
	for scanner.Scan() {
		if counted.bytes > limits.MaxSessionBytes {
			return nil, Stats{}, errors.New("session export exceeds limit")
		}
		line := append([]byte(nil), scanner.Bytes()...)
		if int64(len(line)) > limits.MaxLineBytes {
			return nil, Stats{}, errors.New("session export line exceeds limit")
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !utf8.Valid(line) {
			return nil, Stats{}, errors.New("session export encoding invalid")
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil || event == nil {
			return nil, Stats{}, errors.New("session export JSON invalid")
		}
		rawEvents = append(rawEvents, event)
	}
	if counted.bytes > limits.MaxSessionBytes {
		return nil, Stats{}, errors.New("session export exceeds limit")
	}
	if err := scanner.Err(); err != nil {
		if lineTooLong {
			return nil, Stats{}, errors.New("session export line exceeds limit")
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, Stats{}, err
		}
		return nil, Stats{}, errors.New("invalid session export stream")
	}
	omittedReads := markCorrelatedReadResults(rawEvents)
	pseudonyms := pseudonymizeCorrelationIDs(rawEvents, correlationNamespace)
	events := make([]map[string]any, 0, len(rawEvents))
	total := Stats{OmittedReads: omittedReads, Replacements: pseudonyms}
	for _, event := range rawEvents {
		redacted, stats, err := RedactEvent(event)
		if err != nil {
			return nil, Stats{}, errors.New("session export nesting invalid")
		}
		events = append(events, redacted)
		addStats(&total, stats)
	}
	return events, total, nil
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

type toolCallOccurrence struct {
	ids      []string
	read     bool
	resolved bool
	order    int
}

type toolResultOccurrence struct {
	ids   []string
	value map[string]any
	order int
}

func markCorrelatedReadResults(events []map[string]any) int {
	var calls []*toolCallOccurrence
	var results []toolResultOccurrence
	order := 0
	for _, event := range events {
		walkEventMaps(event, func(value map[string]any) {
			current := order
			order++
			if isToolCallShape(value) {
				if ids := callAssociationIDs(value); len(ids) != 0 {
					calls = append(calls, &toolCallOccurrence{ids: ids, read: isReadTool(value), order: current})
				}
			}
			if isToolResultShape(value) {
				if ids := resultAssociationIDs(value); len(ids) != 0 {
					results = append(results, toolResultOccurrence{ids: ids, value: value, order: current})
				}
			}
		})
	}
	omitted := 0
	callsByID := make(map[string][]*toolCallOccurrence)
	for _, call := range calls {
		for _, id := range call.ids {
			callsByID[id] = append(callsByID[id], call)
		}
	}
	taintedIDs := make(map[string]bool)
	for _, result := range results {
		call, matchedID := nearestCall(callsByID, result)
		if call == nil {
			continue
		}
		if !call.read && !taintedIDs[matchedID] && hasUnresolvedReadBefore(callsByID[matchedID], call.order) {
			taintedIDs[matchedID] = true
		}
		call.resolved = true
		if !call.read && !taintedIDs[matchedID] {
			continue
		}
		for key, current := range result.value {
			if !isReadPayloadKey(key) || current == omittedFileContent {
				continue
			}
			result.value[key] = omittedFileContent
			omitted++
		}
	}
	return omitted
}

func hasUnresolvedReadBefore(calls []*toolCallOccurrence, before int) bool {
	for _, call := range calls {
		if call.order >= before {
			break
		}
		if call.read && !call.resolved {
			return true
		}
	}
	return false
}

func nearestCall(callsByID map[string][]*toolCallOccurrence, result toolResultOccurrence) (*toolCallOccurrence, string) {
	var preceding, following *toolCallOccurrence
	precedingID, followingID := "", ""
	for _, id := range result.ids {
		calls := callsByID[id]
		if len(calls) == 0 {
			continue
		}
		index := sort.Search(len(calls), func(index int) bool { return calls[index].order >= result.order })
		if index > 0 {
			call := calls[index-1]
			if preceding == nil || call.order > preceding.order {
				preceding, precedingID = call, id
			}
		}
		if index < len(calls) {
			call := calls[index]
			if following == nil || call.order < following.order {
				following, followingID = call, id
			}
		}
	}
	if preceding != nil {
		return preceding, precedingID
	}
	return following, followingID
}

func isToolCallShape(value map[string]any) bool {
	if len(callAssociationIDs(value)) == 0 || isToolResultShape(value) {
		return false
	}
	if isReadTool(value) {
		return true
	}
	for _, field := range []string{"type", "role"} {
		name := normalizeName(prioritizedStringField(value, field))
		if name == "tooluse" || name == "toolcall" || name == "functioncall" {
			return true
		}
	}
	return prioritizedStringField(value, "name", "toolname") != ""
}

func callAssociationIDs(input map[string]any) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ids := make([]string, 0, 4)
	for _, wanted := range []string{"toolcallid", "tooluseid", "callid", "id"} {
		for _, key := range keys {
			if normalizeName(key) != wanted {
				continue
			}
			id, ok := input[key].(string)
			if ok && id != "" && !containsString(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func isToolResultShape(value map[string]any) bool {
	for _, field := range []string{"type", "role"} {
		switch normalizeName(prioritizedStringField(value, field)) {
		case "toolresult", "toolresponse", "functioncalloutput":
			return true
		}
	}
	return false
}

func walkEventMaps(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkEventMaps(typed[key], visit)
		}
	case []any:
		for _, child := range typed {
			walkEventMaps(child, visit)
		}
	}
}

func pseudonymizeCorrelationIDs(events []map[string]any, namespace string) int {
	replacements := 0
	for _, event := range events {
		walkEventMaps(event, func(value map[string]any) {
			for key, current := range value {
				if !isCorrelationIDKey(key) {
					continue
				}
				raw, ok := current.(string)
				if !ok || raw == "" {
					continue
				}
				value[key] = correlationPseudonym(namespace, raw)
				replacements++
			}
		})
	}
	return replacements
}

func isCorrelationIDKey(key string) bool {
	switch normalizeName(key) {
	case "id", "callid", "toolcallid", "tooluseid", "parentcallid", "parenttoolcallid", "parenttooluseid":
		return true
	default:
		return false
	}
}

func correlationPseudonym(namespace, raw string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + raw))
	// Encode each nibble as a letter so the stable identifier cannot itself be
	// mistaken for a phone number or another secret by the string redactor.
	encoded := make([]byte, 32)
	for index, value := range sum[:16] {
		encoded[index*2] = 'a' + (value >> 4)
		encoded[index*2+1] = 'a' + (value & 0x0f)
	}
	return "cid_" + string(encoded)
}
