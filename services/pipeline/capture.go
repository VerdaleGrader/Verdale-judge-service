package pipeline

import (
	"strings"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/runner"
)

// capture keeps what a job asked to get back of the programs' output, within
// a budget for the whole job so a result always fits in a callback.
type capture struct {
	stdout    int
	stderr    int
	remaining int
}

// newCapture is nil when the job did not ask for any captured text.
func newCapture(job contract.JobRequest) *capture {
	request := job.Capture
	if request == nil {
		return nil
	}
	c := &capture{remaining: defaultCaptureTotal}
	if request.StdoutBytes != nil {
		c.stdout = *request.StdoutBytes
	}
	if request.StderrBytes != nil {
		c.stderr = *request.StderrBytes
	}
	if request.TotalBytes != nil {
		c.remaining = *request.TotalBytes
	}
	if c.stdout == 0 && c.stderr == 0 {
		return nil
	}
	return c
}

// fits reports whether the whole output is within what may be kept: a
// generated expected output that was cut would be wrong, so it is refused.
func (c *capture) fits(outputPath string) bool {
	if c == nil {
		return false
	}
	size, ok := regularFileSize(outputPath)
	if !ok {
		return true // no output file: the program printed nothing
	}
	return size <= int64(c.stdout) && size <= int64(c.remaining)
}

// record adds the captured text of one test to its result.
func (c *capture) record(result *contract.TestResult, stdoutPath, stderrPath string) {
	if c == nil || result.Verdict == contract.TestVerdictSKIPPED {
		return
	}
	if c.stdout > 0 && result.Verdict != contract.TestVerdictOLE {
		if text, size, ok := c.take(stdoutPath, c.stdout, false); ok {
			result.Stdout, result.StdoutSizeBytes = &text, &size
		}
	}
	if c.stderr > 0 {
		if text, size, ok := c.take(stderrPath, c.stderr, true); ok {
			result.Stderr, result.StderrSizeBytes = &text, &size
		}
	}
}

// take reads the start of a file (a missing one is empty) and charges it to
// the budget. ok is false when the budget is used up.
func (c *capture) take(path string, limit int, scrub bool) (string, int64, bool) {
	if c.remaining <= 0 {
		return "", 0, false
	}
	size, _ := regularFileSize(path)
	keep := min(limit, c.remaining)
	raw, _ := readPrefix(path, int64(keep))
	var text string
	if scrub {
		// Standard error may name host paths; the notice marks a cut.
		text = runner.SanitizeCompilerOutput(raw, keep)
	} else {
		text = runner.TruncateUTF8(strings.ToValidUTF8(string(raw), "\uFFFD"), keep)
	}
	c.remaining -= len(text)
	return text, size, true
}
