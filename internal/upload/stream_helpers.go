package upload

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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

func scanEvents(reader io.Reader, limits Limits) ([]map[string]any, Stats, error) {
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
		events  []map[string]any
		total   Stats
		readIDs = make(map[string]struct{})
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
		collectReadToolIDs(event, readIDs)
		redacted, stats, err := RedactEvent(event)
		if err != nil {
			return nil, Stats{}, errors.New("session export nesting invalid")
		}
		stats.OmittedReads += omitCorrelatedReadResults(redacted, readIDs)
		events = append(events, redacted)
		addStats(&total, stats)
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

func collectReadToolIDs(value any, ids map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		if isReadTool(typed) {
			if id := readCallID(typed); id != "" {
				ids[id] = struct{}{}
			}
		}
		for _, child := range typed {
			collectReadToolIDs(child, ids)
		}
	case []any:
		for _, child := range typed {
			collectReadToolIDs(child, ids)
		}
	}
}

func omitCorrelatedReadResults(value any, ids map[string]struct{}) int {
	omitted := 0
	switch typed := value.(type) {
	case map[string]any:
		if isCorrelatedReadResult(typed, ids) {
			for key := range typed {
				if !isReadPayloadKey(key) {
					continue
				}
				if current, ok := typed[key].(string); ok && current == omittedFileContent {
					continue
				}
				typed[key] = omittedFileContent
				omitted++
			}
		}
		for _, child := range typed {
			omitted += omitCorrelatedReadResults(child, ids)
		}
	case []any:
		for _, child := range typed {
			omitted += omitCorrelatedReadResults(child, ids)
		}
	}
	return omitted
}
