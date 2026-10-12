package jobs

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

func validJob(kind contract.JobKind) contract.JobRequest {
	file := contract.TestFile{Sha256: strings.Repeat("a", 64), SizeBytes: 3}
	return contract.JobRequest{
		JobId: uuid.New(), Kind: kind, Language: contract.LanguageCpp, Source: "int main(){}",
		Limits:      contract.Limits{TimeMs: 1000, MemoryKb: 262144},
		Tests:       []contract.TestCase{{Id: "1", Input: file, Output: &file}},
		CallbackUrl: "http://core/internal/judge/result",
	}
}

func newService() *service {
	return &service{config: Config{AllowedCallbackPrefixes: []string{"http://core/"}, MaxTestFileBytes: 1 << 20}}
}

func fields(details []contract.ErrorDetail) []string {
	var out []string
	for _, detail := range details {
		out = append(out, detail.Field)
	}
	return out
}

func intPtr(value int) *int { return &value }

func TestSubmissionAndVerifyNeedTheExpectedOutput(t *testing.T) {
	for _, kind := range []contract.JobKind{contract.JobKindSubmission, contract.JobKindVerify} {
		request := validJob(kind)
		assert.Empty(t, newService().Validate(request))
		request.Tests[0].Output = nil
		assert.Equal(t, []string{"tests[0].output"}, fields(newService().Validate(request)), string(kind))
	}
}

func TestGenerateNeedsNoExpectedOutputButMustCaptureStdout(t *testing.T) {
	request := validJob(contract.JobKindGenerate)
	request.Tests[0].Output = nil

	assert.Equal(t, []string{"capture.stdout_bytes"}, fields(newService().Validate(request)), "no capture")

	request.Capture = &contract.Capture{StdoutBytes: intPtr(0)}
	assert.Equal(t, []string{"capture.stdout_bytes"}, fields(newService().Validate(request)), "capture of nothing")

	request.Capture = &contract.Capture{StdoutBytes: intPtr(1024)}
	assert.Empty(t, newService().Validate(request))
}

func TestCaptureRangesAreChecked(t *testing.T) {
	request := validJob(contract.JobKindSubmission)
	request.Capture = &contract.Capture{StdoutBytes: intPtr(MaxCaptureStdoutBytes + 1), StderrBytes: intPtr(-1), TotalBytes: intPtr(0)}
	assert.ElementsMatch(t,
		[]string{"capture.stdout_bytes", "capture.stderr_bytes", "capture.total_bytes"},
		fields(newService().Validate(request)))

	request.Capture = &contract.Capture{StdoutBytes: intPtr(MaxCaptureStdoutBytes), StderrBytes: intPtr(MaxCaptureStderrBytes), TotalBytes: intPtr(MaxCaptureTotalBytes)}
	assert.Empty(t, newService().Validate(request))
}

func TestUnknownKindIsRefused(t *testing.T) {
	request := validJob("rejudge")
	assert.Equal(t, []string{"kind"}, fields(newService().Validate(request)))
}
