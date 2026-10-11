// Package comparator decides whether a program's output matches the
// expected output. Checkers are looked up by name so new ones (exact,
// token, float tolerance, custom) can be added without touching callers.
package comparator

import (
	"bufio"
	"fmt"
	"io"
)

// Result of a check. Message says where the outputs first differ; it is
// shown to solvers only for sample tests.
type Result struct {
	Accepted bool
	Message  string
}

// Checker compares expected and actual output streams. Implementations
// must stream: outputs can be as large as the output limit (64 MiB).
type Checker interface {
	Check(expected, actual io.Reader) (Result, error)
}

var registry = map[string]Checker{
	"line": LineChecker{},
}

// Get returns the checker registered under name.
func Get(name string) (Checker, error) {
	checker, found := registry[name]
	if !found {
		return nil, fmt.Errorf("unknown checker %q", name)
	}
	return checker, nil
}

// LineChecker accepts output that equals the expected output after removing
// trailing whitespace (space, tab, CR, vertical tab, form feed) at the end
// of every line and blank lines at the end of the file. Leading whitespace
// and whitespace inside a line are significant.
type LineChecker struct{}

func (LineChecker) Check(expected, actual io.Reader) (Result, error) {
	expectedStream := newNormalizer(expected)
	actualStream := newNormalizer(actual)
	line := 1
	for {
		expectedByte, expectedErr := expectedStream.next()
		actualByte, actualErr := actualStream.next()
		if expectedErr != nil && expectedErr != io.EOF {
			return Result{}, fmt.Errorf("read expected output: %w", expectedErr)
		}
		if actualErr != nil && actualErr != io.EOF {
			return Result{}, fmt.Errorf("read program output: %w", actualErr)
		}
		switch {
		case expectedErr == io.EOF && actualErr == io.EOF:
			return Result{Accepted: true}, nil
		case expectedErr == io.EOF:
			return Result{Message: fmt.Sprintf("Line %d: the output has extra content after the expected end.", lineOf(line, actualByte))}, nil
		case actualErr == io.EOF:
			return Result{Message: fmt.Sprintf("Line %d: the output ended before the expected output.", lineOf(line, expectedByte))}, nil
		case expectedByte != actualByte:
			return Result{Message: fmt.Sprintf("Line %d differs from the expected output.", line)}, nil
		}
		if expectedByte == '\n' {
			line++
		}
	}
}

// lineOf reports the line where the remaining content starts: when one
// stream ended right before a line break in the other, the extra or missing
// content is on the following line.
func lineOf(line int, next byte) int {
	if next == '\n' {
		return line + 1
	}
	return line
}

func isTrailingSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\v' || value == '\f'
}

// normalizer yields the bytes of a stream with trailing whitespace on each
// line and trailing blank lines removed. Whitespace and newlines are held
// back (as counts plus the exact bytes of the current run) and only
// released once a visible byte proves they were not trailing.
type normalizer struct {
	reader *bufio.Reader
	// pendingNewlines is the number of line breaks seen since the last
	// visible byte; pendingSpace holds the whitespace after the last of them.
	pendingNewlines int
	pendingSpace    []byte
	// emit is a queue of bytes ready to hand out.
	emit []byte
}

func newNormalizer(reader io.Reader) *normalizer {
	return &normalizer{reader: bufio.NewReaderSize(reader, 64*1024)}
}

func (n *normalizer) next() (byte, error) {
	for len(n.emit) == 0 {
		value, err := n.reader.ReadByte()
		if err != nil {
			// At end of input everything still pending is trailing: drop it.
			return 0, err
		}
		switch {
		case value == '\n':
			n.pendingNewlines++
			n.pendingSpace = n.pendingSpace[:0]
		case isTrailingSpace(value):
			n.pendingSpace = append(n.pendingSpace, value)
		default:
			for ; n.pendingNewlines > 0; n.pendingNewlines-- {
				n.emit = append(n.emit, '\n')
			}
			n.emit = append(n.emit, n.pendingSpace...)
			n.pendingSpace = n.pendingSpace[:0]
			n.emit = append(n.emit, value)
		}
	}
	value := n.emit[0]
	n.emit = n.emit[1:]
	if len(n.emit) == 0 {
		n.emit = n.emit[:0:cap(n.emit)]
	}
	return value, nil
}
