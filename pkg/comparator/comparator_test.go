package comparator

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func check(t *testing.T, expected, actual string) Result {
	t.Helper()
	checker, err := Get("line")
	require.NoError(t, err)
	result, err := checker.Check(strings.NewReader(expected), strings.NewReader(actual))
	require.NoError(t, err)
	return result
}

func TestLineCheckerAccepts(t *testing.T) {
	cases := map[string][2]string{
		"identical":                        {"1 2\n3\n", "1 2\n3\n"},
		"missing final newline":            {"1 2\n3\n", "1 2\n3"},
		"trailing spaces and tabs":         {"1 2\n3\n", "1 2  \t\n3 \n"},
		"windows line endings":             {"1 2\n3\n", "1 2\r\n3\r\n"},
		"extra blank lines at end":         {"5\n", "5\n\n\n   \n"},
		"expected has trailing blank line": {"5\n\n", "5"},
		"both empty":                       {"", ""},
		"empty versus only whitespace":     {"", " \n\n\t\n"},
		"blank lines in the middle kept":   {"a\n\nb\n", "a\n  \nb\n"},
		"thai text":                        {"สวัสดี\n", "สวัสดี  \n"},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			assert.True(t, check(t, pair[0], pair[1]).Accepted)
		})
	}
}

func TestLineCheckerRejects(t *testing.T) {
	cases := map[string]struct {
		expected, actual, message string
	}{
		"different value":               {"1 2\n3\n", "1 2\n4\n", "Line 2 differs"},
		"empty output":                  {"42\n", "", "Line 1: the output ended"},
		"too many lines":                {"1\n2\n", "1\n2\n3\n", "Line 3: the output has extra content"},
		"too few lines":                 {"1\n2\n3\n", "1\n2\n", "Line 3: the output ended"},
		"leading space is significant":  {"1 2\n", " 1 2\n", "Line 1 differs"},
		"inner spacing is significant":  {"1 2\n", "1  2\n", "Line 1 differs"},
		"missing blank line in middle":  {"a\n\nb\n", "a\nb\n", "Line 2 differs"},
		"extra space before value":      {"a\nb\n", "a\n b\n", "Line 2 differs"},
		"output where nothing expected": {"", "x\n", "Line 1: the output has extra content"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			result := check(t, testCase.expected, testCase.actual)
			assert.False(t, result.Accepted)
			assert.Contains(t, result.Message, testCase.message)
		})
	}
}

// repeatReader produces count copies of chunk without allocating them all,
// so the test proves the checker streams instead of buffering.
type repeatReader struct {
	chunk []byte
	count int
	pos   int
}

func (r *repeatReader) Read(buffer []byte) (int, error) {
	if r.count == 0 {
		return 0, io.EOF
	}
	written := copy(buffer, r.chunk[r.pos:])
	r.pos += written
	if r.pos == len(r.chunk) {
		r.pos = 0
		r.count--
	}
	return written, nil
}

func TestLineCheckerStreamsLargeOutput(t *testing.T) {
	line := []byte("1000000000 2000000000 3000000000\n")
	const lines = 2_000_000 // about 66 MB per side
	checker := LineChecker{}
	result, err := checker.Check(&repeatReader{chunk: line, count: lines}, &repeatReader{chunk: line, count: lines})
	require.NoError(t, err)
	assert.True(t, result.Accepted)

	result, err = checker.Check(&repeatReader{chunk: line, count: lines}, &repeatReader{chunk: line, count: lines + 1})
	require.NoError(t, err)
	assert.False(t, result.Accepted)
	assert.Contains(t, result.Message, "Line 2000001")
}

func TestUnknownChecker(t *testing.T) {
	_, err := Get("float")
	assert.Error(t, err)
}
