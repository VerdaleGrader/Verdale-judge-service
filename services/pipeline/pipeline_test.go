package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/configs"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/runner"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
)

// fakeSandbox simulates isolate: a compile run writes the artifact, and a
// test run looks up what to "print" for the input it was given.
type fakeSandbox struct {
	t          *testing.T
	mu         sync.Mutex
	compileOK  bool
	compileLog string
	// byInput maps test input content to the simulated run.
	byInput    map[string]fakeRun
	acquired   int
	cleaned    int
	acquireErr error
}

type fakeRun struct {
	status sandbox.Status
	output string
	stderr string
	cpu    time.Duration
	rssKB  int
	err    error
}

func (s *fakeSandbox) CheckEnvironment(context.Context) error { return nil }

func (s *fakeSandbox) Acquire(context.Context) (sandbox.Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquireErr != nil {
		return nil, s.acquireErr
	}
	s.acquired++
	return &fakeBox{sandbox: s, dir: s.t.TempDir()}, nil
}

type fakeBox struct {
	sandbox *fakeSandbox
	dir     string
}

func (b *fakeBox) Dir() string { return b.dir }

func (b *fakeBox) Cleanup() error {
	b.sandbox.mu.Lock()
	defer b.sandbox.mu.Unlock()
	b.sandbox.cleaned++
	return nil
}

func (b *fakeBox) Run(_ context.Context, request sandbox.RunRequest) (sandbox.RunResult, error) {
	if request.Stdin == "" { // compile step
		_ = os.WriteFile(filepath.Join(b.dir, request.Stdout), []byte(b.sandbox.compileLog), 0o644)
		if !b.sandbox.compileOK {
			return sandbox.RunResult{Status: sandbox.StatusRuntimeError, ExitCode: 1}, nil
		}
		_ = os.WriteFile(filepath.Join(b.dir, "main"), []byte("binary"), 0o755)
		return sandbox.RunResult{Status: sandbox.StatusOK, CPUTime: 300 * time.Millisecond}, nil
	}
	input, _ := os.ReadFile(filepath.Join(b.dir, request.Stdin))
	run, found := b.sandbox.byInput[string(input)]
	if !found {
		b.sandbox.t.Fatalf("unexpected input %q", input)
	}
	if run.err != nil {
		return sandbox.RunResult{Status: sandbox.StatusInternal}, run.err
	}
	_ = os.WriteFile(filepath.Join(b.dir, request.Stdout), []byte(run.output), 0o644)
	if request.Stderr != "" && run.stderr != "" {
		_ = os.WriteFile(filepath.Join(b.dir, request.Stderr), []byte(run.stderr), 0o644)
	}
	return sandbox.RunResult{Status: run.status, CPUTime: run.cpu, MaxRSSKB: run.rssKB, ExitCode: 1, Signal: 11}, nil
}

// mapFetcher serves test files from memory, keyed by sha256 = content here.
type mapFetcher struct {
	t        *testing.T
	failWith error
}

func (f mapFetcher) Fetch(_ context.Context, file contract.TestFile) (string, func(), error) {
	if f.failWith != nil {
		return "", nil, f.failWith
	}
	path := filepath.Join(f.t.TempDir(), "file")
	require.NoError(f.t, os.WriteFile(path, []byte(file.Sha256), 0o644))
	return path, func() {}, nil
}

func newPipeline(t *testing.T, fake *fakeSandbox, fetcher TestFileFetcher) *Pipeline {
	languages := runner.Languages(configs.LanguagesConfig{
		C: configs.LanguageConfig{Binary: "gcc"}, Cpp: configs.LanguageConfig{Binary: "g++"}, Python: configs.LanguageConfig{Binary: "python3"},
	})
	pipeline := New(fake, fetcher, languages, Config{
		WorkDir: t.TempDir(), Path: "/usr/bin:/bin", ExtraTime: 500 * time.Millisecond, WallTimeFactor: 3, OpenFiles: 64,
		Compile:      CompileLimits{CPUTime: 10 * time.Second, WallTime: 20 * time.Second, MemoryKB: 524288, Processes: 64, OutputKB: 1024, MessageLimitBytes: 8192},
		JudgeVersion: "test",
	})
	pipeline.now = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
	return pipeline
}

// testCase uses the content itself as the "sha256" so mapFetcher can serve it.
func testCase(id, input, expected string, sample bool) contract.TestCase {
	return contract.TestCase{
		Id:     id,
		Input:  contract.TestFile{Sha256: input, SizeBytes: int64(len(input))},
		Output: &contract.TestFile{Sha256: expected, SizeBytes: int64(len(expected))},
		Sample: &sample,
	}
}

func job(kind contract.JobKind, tests ...contract.TestCase) contract.JobRequest {
	return contract.JobRequest{
		JobId: uuid.New(), Kind: kind, Language: contract.LanguageCpp, Source: "int main(){}",
		Limits: contract.Limits{TimeMs: 1000, MemoryKb: 262144}, Tests: tests, CallbackUrl: "http://core/",
	}
}

func verdicts(result contract.JobResult) string {
	var parts []string
	for _, test := range result.Tests {
		parts = append(parts, test.Id+"="+string(test.Verdict))
	}
	return strings.Join(parts, " ")
}

func TestAllTestsPass(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusOK, output: "out1 \n\n", cpu: 120 * time.Millisecond, rssKB: 3000},
		"in2": {status: sandbox.StatusOK, output: "out2", cpu: 80 * time.Millisecond, rssKB: 5000},
	}}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(),
		job(contract.JobKindSubmission, testCase("1", "in1", "out1\n", true), testCase("2", "in2", "out2\n", false)))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictAC, result.Verdict)
	assert.Equal(t, "1=AC 2=AC", verdicts(result))
	assert.Equal(t, contract.ResultSummary{Passed: 2, Total: 2, TimeMs: 120, MemoryKb: 5000}, result.Summary)
	assert.True(t, result.Compile.Ok)
	assert.Equal(t, fake.acquired, fake.cleaned, "every box is cleaned up")
	assert.Equal(t, 3, fake.acquired, "a fresh box for compile and for every test")

	require.NotNil(t, result.Tests[0].StdoutPreview, "sample tests show their output")
	assert.Equal(t, "out1 \n\n", *result.Tests[0].StdoutPreview)
	assert.Nil(t, result.Tests[1].StdoutPreview, "hidden test output never leaves Judge")
}

func TestSubmissionStopsAtFirstFailure(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusOK, output: "ok"},
		"in2": {status: sandbox.StatusOK, output: "wrong"},
		"in3": {status: sandbox.StatusOK, output: "ok"},
	}}
	tests := []contract.TestCase{testCase("1", "in1", "ok", false), testCase("2", "in2", "right", false), testCase("3", "in3", "ok", false)}

	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindSubmission, tests...))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictWA, result.Verdict)
	assert.Equal(t, "1=AC 2=WA 3=SKIPPED", verdicts(result))
	assert.Equal(t, 1, result.Summary.Passed)
	assert.Nil(t, result.Tests[1].CheckerMessage, "no checker message for hidden tests")

	result, err = newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindVerify, tests...))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictWA, result.Verdict)
	assert.Equal(t, "1=AC 2=WA 3=AC", verdicts(result), "verify runs every test")
	assert.Equal(t, 2, result.Summary.Passed)
}

func TestVerdictMapping(t *testing.T) {
	cases := map[sandbox.Status]contract.Verdict{
		sandbox.StatusTimeLimit:    contract.VerdictTLE,
		sandbox.StatusMemoryLimit:  contract.VerdictMLE,
		sandbox.StatusRuntimeError: contract.VerdictRE,
		sandbox.StatusSignaled:     contract.VerdictRE,
		sandbox.StatusOutputLimit:  contract.VerdictOLE,
	}
	for status, verdict := range cases {
		t.Run(string(status), func(t *testing.T) {
			fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{"in": {status: status}}}
			result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindSubmission, testCase("1", "in", "x", true)))
			require.NoError(t, err)
			assert.Equal(t, verdict, result.Verdict)
		})
	}
}

func TestFullOutputIsOLEEvenWhenTheProgramExitsWithAnError(t *testing.T) {
	// Python ignores SIGXFSZ: it exits with code 1 once writing fails.
	full := strings.Repeat("9", 2048)
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{"in": {status: sandbox.StatusRuntimeError, output: full}}}
	request := job(contract.JobKindSubmission, testCase("1", "in", "x", false))
	outputKB := 2
	request.Limits.OutputKb = &outputKB
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictOLE, result.Verdict)
}

func TestWrongAnswerOnSampleExplainsWhere(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{"in": {status: sandbox.StatusOK, output: "1\n3\n"}}}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindSubmission, testCase("s1", "in", "1\n2\n", true)))
	require.NoError(t, err)
	require.NotNil(t, result.Tests[0].CheckerMessage)
	assert.Contains(t, *result.Tests[0].CheckerMessage, "Line 2")
}

func TestCompilationError(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: false, compileLog: "/box/main.cpp:1:1: error: expected ';' before '}'\n"}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindSubmission, testCase("1", "in", "out", false)))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictCE, result.Verdict)
	assert.False(t, result.Compile.Ok)
	assert.Equal(t, "main.cpp:1:1: error: expected ';' before '}'\n", result.Compile.Message)
	assert.Empty(t, result.Tests)
	assert.Equal(t, 1, result.Summary.Total)
}

func TestSystemErrorsAreNotVerdicts(t *testing.T) {
	storageDown := errors.New("storage unavailable after 4 attempts")
	_, err := newPipeline(t, &fakeSandbox{t: t, compileOK: true}, mapFetcher{t: t, failWith: storageDown}).
		Execute(context.Background(), job(contract.JobKindSubmission, testCase("1", "in", "out", false)))
	var systemErr *SystemError
	require.ErrorAs(t, err, &systemErr)
	assert.Equal(t, CodeTestFileUnavailable, systemErr.Code)

	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{"in": {err: errors.New("isolate exit 2")}}}
	_, err = newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), job(contract.JobKindSubmission, testCase("1", "in", "out", false)))
	require.ErrorAs(t, err, &systemErr)
	assert.Equal(t, CodeSandboxFailure, systemErr.Code)
	assert.Equal(t, fake.acquired, fake.cleaned, "boxes are cleaned up on failure too")
}

// countingFetcher records every file asked for.
type countingFetcher struct {
	mapFetcher
	fetched []string
}

func (f *countingFetcher) Fetch(ctx context.Context, file contract.TestFile) (string, func(), error) {
	f.fetched = append(f.fetched, file.Sha256)
	return f.mapFetcher.Fetch(ctx, file)
}

func withCapture(request contract.JobRequest, stdout, stderr int, total *int) contract.JobRequest {
	request.Capture = &contract.Capture{StdoutBytes: &stdout, StderrBytes: &stderr, TotalBytes: total}
	return request
}

func TestCaptureReturnsOutputOfHiddenTestsToo(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusOK, output: "ok\n"},
		"in2": {status: sandbox.StatusOK, output: "wrong\n", stderr: "warning: /box/main.cpp:3 careful\n"},
	}}
	request := withCapture(job(contract.JobKindSubmission,
		testCase("1", "in1", "ok\n", true), testCase("2", "in2", "right\n", false)), 1024, 1024, nil)

	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "1=AC 2=WA", verdicts(result))

	hidden := result.Tests[1]
	require.NotNil(t, hidden.Stdout)
	assert.Equal(t, "wrong\n", *hidden.Stdout, "the program output of a hidden test is sent when Core asks for it")
	require.NotNil(t, hidden.StdoutSizeBytes)
	assert.Equal(t, int64(6), *hidden.StdoutSizeBytes)
	require.NotNil(t, hidden.Stderr)
	assert.Equal(t, "warning: main.cpp:3 careful\n", *hidden.Stderr, "host paths are removed from standard error")
	assert.Nil(t, hidden.StdoutPreview, "the preview stays for sample tests only")
	assert.Nil(t, hidden.CheckerMessage)

	sample := result.Tests[0]
	require.NotNil(t, sample.Stdout)
	assert.Equal(t, "ok\n", *sample.Stdout)
	require.NotNil(t, sample.StdoutPreview)
}

func TestWithoutCaptureHiddenOutputNeverLeaves(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in": {status: sandbox.StatusOK, output: "secret\n", stderr: "boom"},
	}}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(),
		job(contract.JobKindSubmission, testCase("1", "in", "other\n", false)))
	require.NoError(t, err)
	assert.Nil(t, result.Tests[0].Stdout)
	assert.Nil(t, result.Tests[0].Stderr)
	assert.Nil(t, result.Tests[0].StdoutPreview)
}

func TestCaptureKeepsTheStartAndSaysHowBigTheOutputWas(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10)
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{"in": {status: sandbox.StatusOK, output: long}}}
	request := withCapture(job(contract.JobKindSubmission, testCase("1", "in", "x", false)), 25, 0, nil)
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, long[:25], *result.Tests[0].Stdout)
	assert.Equal(t, int64(100), *result.Tests[0].StdoutSizeBytes)
	assert.Nil(t, result.Tests[0].Stderr, "stderr_bytes of 0 keeps none")
}

func TestCaptureNeverSplitsACharacterAndFixesBrokenBytes(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in": {status: sandbox.StatusOK, output: "ก" + "ข" + "\xff!"},
	}}
	request := withCapture(job(contract.JobKindVerify, testCase("1", "in", "x", false)), 4, 0, nil)
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "ก", *result.Tests[0].Stdout, "4 bytes hold one 3-byte Thai letter and no half of the next")

	request = withCapture(job(contract.JobKindVerify, testCase("1", "in", "x", false)), 100, 0, nil)
	result, err = newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "กข\uFFFD!", *result.Tests[0].Stdout)
}

func TestCaptureStopsWhenTheBudgetIsUsedUp(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusOK, output: "1234567890"},
		"in2": {status: sandbox.StatusOK, output: "abcdefghij"},
		"in3": {status: sandbox.StatusOK, output: "ABCDEFGHIJ"},
	}}
	total := 15
	request := withCapture(job(contract.JobKindVerify,
		testCase("1", "in1", "x", false), testCase("2", "in2", "x", false), testCase("3", "in3", "x", false)),
		100, 0, &total)
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "1234567890", *result.Tests[0].Stdout)
	assert.Equal(t, "abcde", *result.Tests[1].Stdout, "the rest of the budget")
	assert.Nil(t, result.Tests[2].Stdout, "nothing is left for the third test")
}

func TestOutputLimitTestsDoNotCaptureAGiantOutput(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in": {status: sandbox.StatusOutputLimit, output: "xxxx"},
	}}
	request := withCapture(job(contract.JobKindVerify, testCase("1", "in", "x", false)), 100, 0, nil)
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, contract.TestVerdictOLE, result.Tests[0].Verdict)
	assert.Nil(t, result.Tests[0].Stdout)
}

func generateJob(stdoutBytes int, tests ...contract.TestCase) contract.JobRequest {
	request := withCapture(job(contract.JobKindGenerate, tests...), stdoutBytes, 256, nil)
	for index := range request.Tests {
		request.Tests[index].Output = nil
	}
	return request
}

func inputOnly(id, input string) contract.TestCase {
	return contract.TestCase{Id: id, Input: contract.TestFile{Sha256: input, SizeBytes: int64(len(input))}}
}

func TestGenerateReturnsTheWholeOutputWithoutComparing(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusOK, output: "3\n", cpu: 10 * time.Millisecond},
		"in2": {status: sandbox.StatusOK, output: "7\n"},
	}}
	fetcher := &countingFetcher{mapFetcher: mapFetcher{t: t}}
	result, err := newPipeline(t, fake, fetcher).Execute(context.Background(),
		generateJob(1024, inputOnly("1", "in1"), inputOnly("2", "in2")))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictAC, result.Verdict)
	assert.Equal(t, "1=AC 2=AC", verdicts(result))
	assert.Equal(t, "3\n", *result.Tests[0].Stdout)
	assert.Equal(t, "7\n", *result.Tests[1].Stdout)
	assert.Equal(t, []string{"in1", "in2"}, fetcher.fetched, "only the inputs are downloaded")
	assert.Equal(t, 2, result.Summary.Passed)
}

func TestGenerateRunsEveryTestEvenAfterAFailureAndReportsWhy(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in1": {status: sandbox.StatusTimeLimit},
		"in2": {status: sandbox.StatusRuntimeError, stderr: "Traceback: /box/main.py line 2\n"},
		"in3": {status: sandbox.StatusOK, output: "fine\n"},
	}}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(),
		generateJob(1024, inputOnly("1", "in1"), inputOnly("2", "in2"), inputOnly("3", "in3")))
	require.NoError(t, err)
	assert.Equal(t, "1=TLE 2=RE 3=AC", verdicts(result))
	assert.Equal(t, contract.VerdictTLE, result.Verdict)
	assert.Equal(t, "Traceback: main.py line 2\n", *result.Tests[1].Stderr)
	assert.Equal(t, "fine\n", *result.Tests[2].Stdout)
}

func TestGenerateRefusesAnOutputThatWouldBeCut(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: true, byInput: map[string]fakeRun{
		"in": {status: sandbox.StatusOK, output: strings.Repeat("9", 50)},
	}}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(),
		generateJob(10, inputOnly("1", "in")))
	require.NoError(t, err)
	assert.Equal(t, "1=OLE", verdicts(result))
	assert.Nil(t, result.Tests[0].Stdout, "an incomplete output is never offered as the answer")
}

func TestGenerateWithACompilationErrorReportsIt(t *testing.T) {
	fake := &fakeSandbox{t: t, compileOK: false, compileLog: "main.cpp:1: error\n"}
	result, err := newPipeline(t, fake, mapFetcher{t: t}).Execute(context.Background(),
		generateJob(1024, inputOnly("1", "in")))
	require.NoError(t, err)
	assert.Equal(t, contract.VerdictCE, result.Verdict)
	assert.False(t, result.Compile.Ok)
}
