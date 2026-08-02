// Package sharedclient contains bounded, product-neutral readers for the
// on-disk formats shared by the Lingma and Qoder clients.
package sharedclient

import (
	"context"
	"errors"
	"io"
)

var (
	ErrBudgetExceeded = errors.New("sharedclient: read budget exceeded")
	ErrInvalidLimits  = errors.New("sharedclient: invalid limits")
)

// Limits bounds both JSONL framing and SQLite snapshot reads. Callers need
// only populate the fields used by the reader they invoke.
type Limits struct {
	MaxTotalBytes     int64
	MaxLineBytes      int
	MaxRecords        int
	MaxDatabaseBytes  int64
	MaxSessions       int
	MaxRows           int
	MaxPayloadBytes   int64
	MaxCanonicalBytes int64
}

// JSONLLine is one non-empty physical line. Bytes excludes the trailing LF.
// Empty lines are deliberately skipped and do not consume the record budget,
// while Number still identifies the physical line in the input.
type JSONLLine struct {
	Number            int
	Bytes             []byte
	FinalUnterminated bool
}

// WalkJSONL performs bounded line framing only. It does not validate JSON, and
// leaves policy for a final unterminated record to the caller.
func WalkJSONL(ctx context.Context, reader io.Reader, limits Limits, visit func(JSONLLine) error) (err error) {
	if ctx == nil || reader == nil || visit == nil || limits.MaxTotalBytes <= 0 || limits.MaxLineBytes <= 0 || limits.MaxRecords <= 0 {
		return ErrInvalidLimits
	}
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	bufferSize := 32 << 10
	if limits.MaxTotalBytes < int64(bufferSize) {
		bufferSize = int(limits.MaxTotalBytes + 1)
	}
	buffer := make([]byte, bufferSize)
	line := make([]byte, 0, min(limits.MaxLineBytes, 4096))
	total := int64(0)
	lineNumber := 1
	records := 0
	emptyReads := 0

	emit := func(finalUnterminated bool) error {
		if len(line) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		records++
		if records > limits.MaxRecords {
			return ErrBudgetExceeded
		}
		framed := JSONLLine{Number: lineNumber, Bytes: line, FinalUnterminated: finalUnterminated}
		if err := visit(framed); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		line = make([]byte, 0, min(limits.MaxLineBytes, 4096))
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		readBuffer := buffer
		allowance := limits.MaxTotalBytes - total
		if allowance < int64(len(readBuffer)) {
			readBuffer = readBuffer[:int(allowance)+1]
		}
		n, readErr := reader.Read(readBuffer)
		if err := ctx.Err(); err != nil {
			return err
		}
		if n < 0 || n > len(readBuffer) {
			return errors.New("sharedclient: invalid reader result")
		}
		if n == 0 && readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
			continue
		}
		emptyReads = 0
		total += int64(n)
		if total > limits.MaxTotalBytes {
			return ErrBudgetExceeded
		}
		for _, character := range readBuffer[:n] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if character == '\n' {
				if err := emit(false); err != nil {
					return err
				}
				lineNumber++
				continue
			}
			if len(line) == limits.MaxLineBytes {
				return ErrBudgetExceeded
			}
			line = append(line, character)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			if err := emit(true); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}
