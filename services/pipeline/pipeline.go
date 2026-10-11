// Package pipeline judges one job: fetch tests, compile, run every test in
// a fresh sandbox, compare the output, and decide the verdict.
//
// Execute returns an error only for system failures (storage down, sandbox
// broken). Those are never the submitter's fault: the worker retries the
// job and, if it keeps failing, reports ISE so Core re-judges it later.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kanonkc/verdale-judge-service/pkg/comparator"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/runner"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
)

// System error codes reported in JobResult.error for ISE.
const (
	CodeTestFileUnavailable   = "TEST_FILE_UNAVAILABLE"
	CodeSandboxFailure        = "SANDBOX_FAILURE"
	CodeMaxDeliveriesExceeded = "MAX_DELIVERIES_EXCEEDED"
	CodeInvalidJob            = "INVALID_JOB"
)

const (
	defaultOutputKB    = 65536
	stdoutPreviewBytes = 1024
	checkerMessageMax  = 256
	inputFile          = "input.txt"
	outputFile         = "output.txt"
	stderrFile         = "stderr.txt"
	compileLogFile     = "compile.log"
)

// SystemError is a failure of Judge itself.
type SystemError struct {
	Code string
	Err  error
}

func (e *SystemError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *SystemError) Unwrap() error { return e.Err }

func systemError(code string, format string, args ...any) error {
	return &SystemError{Code: code, Err: fmt.Errorf(format, args...)}
}

// TestFileFetcher returns a local path to a verified test file.
type TestFileFetcher interface {
	Fetch(ctx context.Context, file contract.TestFile) (path string, release func(), err error)
}

type Config struct {
	WorkDir        string
	Path           string
	ExtraTime      time.Duration
	WallTimeFactor int
	OpenFiles      int
	Compile        CompileLimits
	JudgeVersion   string
}

type CompileLimits struct {
	CPUTime           time.Duration
	WallTime          time.Duration
	MemoryKB          int
	Processes         int
	OutputKB          int
	MessageLimitBytes int
}

type Pipeline struct {
	sandbox   sandbox.Sandbox
	fetcher   TestFileFetcher
	languages map[contract.Language]runner.Language
	config    Config
	now       func() time.Time
}

func New(sandbox sandbox.Sandbox, fetcher TestFileFetcher, languages map[contract.Language]runner.Language, config Config) *Pipeline {
	return &Pipeline{sandbox: sandbox, fetcher: fetcher, languages: languages, config: config, now: time.Now}
}

// Execute judges the job.
func (p *Pipeline) Execute(ctx context.Context, job contract.JobRequest) (contract.JobResult, error) {
	language, supported := p.languages[job.Language]
	if !supported {
		return contract.JobResult{}, systemError(CodeInvalidJob, "unsupported language %q", job.Language)
	}
	checkerName := string(contract.CheckerLine)
	if job.Checker != nil {
		checkerName = string(*job.Checker)
	}
	checker, err := comparator.Get(checkerName)
	if err != nil {
		return contract.JobResult{}, systemError(CodeInvalidJob, "%v", err)
	}

	workDir, err := os.MkdirTemp(p.config.WorkDir, "job-")
	if err != nil {
		return contract.JobResult{}, systemError(CodeSandboxFailure, "create work dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	// Fetch every file first: a storage outage must not produce a verdict.
	inputs := make([]string, len(job.Tests))
	expected := make([]string, len(job.Tests))
	for index, test := range job.Tests {
		inputPath, releaseInput, err := p.fetcher.Fetch(ctx, test.Input)
		if err != nil {
			return contract.JobResult{}, systemError(CodeTestFileUnavailable, "test %s input: %v", test.Id, err)
		}
		defer releaseInput()
		outputPath, releaseOutput, err := p.fetcher.Fetch(ctx, test.Output)
		if err != nil {
			return contract.JobResult{}, systemError(CodeTestFileUnavailable, "test %s output: %v", test.Id, err)
		}
		defer releaseOutput()
		inputs[index], expected[index] = inputPath, outputPath
	}

	result := contract.JobResult{
		JobId:        job.JobId,
		Kind:         job.Kind,
		JudgeVersion: p.config.JudgeVersion,
		Tests:        []contract.TestResult{},
		Summary:      contract.ResultSummary{Total: len(job.Tests)},
	}

	compileInfo, artifactPath, err := p.compile(ctx, language, job.Source, workDir)
	if err != nil {
		return contract.JobResult{}, err
	}
	result.Compile = &compileInfo
	if !compileInfo.Ok {
		result.Verdict = contract.VerdictCE
		result.JudgedAt = p.now().UTC()
		return result, nil
	}

	stopOnFailure := job.Kind == contract.JobKindSubmission
	stopped := false
	for index, test := range job.Tests {
		if stopped {
			result.Tests = append(result.Tests, contract.TestResult{Id: test.Id, Verdict: contract.TestVerdictSKIPPED})
			continue
		}
		testResult, err := p.runTest(ctx, language, artifactPath, job.Limits, test, inputs[index], expected[index], checker)
		if err != nil {
			return contract.JobResult{}, err
		}
		result.Tests = append(result.Tests, testResult)
		result.Summary.TimeMs = max(result.Summary.TimeMs, testResult.TimeMs)
		result.Summary.MemoryKb = max(result.Summary.MemoryKb, testResult.MemoryKb)
		if testResult.Verdict == contract.TestVerdictAC {
			result.Summary.Passed++
		} else if stopOnFailure {
			stopped = true
		}
	}
	result.Verdict = overallVerdict(result.Tests)
	result.JudgedAt = p.now().UTC()
	return result, nil
}

// overallVerdict is AC when every test passed, otherwise the verdict of the
// first test (in request order) that did not.
func overallVerdict(tests []contract.TestResult) contract.Verdict {
	for _, test := range tests {
		if test.Verdict != contract.TestVerdictAC && test.Verdict != contract.TestVerdictSKIPPED {
			return contract.Verdict(test.Verdict)
		}
	}
	return contract.VerdictAC
}

func (p *Pipeline) compile(ctx context.Context, language runner.Language, source, workDir string) (contract.CompileInfo, string, error) {
	box, err := p.sandbox.Acquire(ctx)
	if err != nil {
		return contract.CompileInfo{}, "", systemError(CodeSandboxFailure, "acquire compile box: %v", err)
	}
	defer box.Cleanup()

	if err := os.WriteFile(filepath.Join(box.Dir(), language.SourceFile), []byte(source), 0o644); err != nil {
		return contract.CompileInfo{}, "", systemError(CodeSandboxFailure, "write source: %v", err)
	}
	limits := p.config.Compile
	run, err := box.Run(ctx, sandbox.RunRequest{
		Args:           language.CompileArgs,
		Stdout:         compileLogFile,
		StderrToStdout: true,
		Env:            []string{"PATH=" + p.config.Path, "HOME=/box", "TMPDIR=/tmp"},
		Limits: sandbox.Limits{
			CPUTime: limits.CPUTime, WallTime: limits.WallTime, MemoryKB: limits.MemoryKB,
			Processes: limits.Processes, FileSizeKB: limits.OutputKB, OpenFiles: p.config.OpenFiles,
		},
	})
	if err != nil {
		return contract.CompileInfo{}, "", systemError(CodeSandboxFailure, "compile: %v", err)
	}
	log, _ := readPrefix(filepath.Join(box.Dir(), compileLogFile), int64(limits.MessageLimitBytes)*2)
	info := contract.CompileInfo{
		Ok:      run.Status == sandbox.StatusOK,
		Message: runner.SanitizeCompilerOutput(log, limits.MessageLimitBytes),
		TimeMs:  int(run.CPUTime.Milliseconds()),
	}
	if !info.Ok {
		switch run.Status {
		case sandbox.StatusTimeLimit:
			info.Message = appendLine(info.Message, "Compilation exceeded the time limit.")
		case sandbox.StatusMemoryLimit:
			info.Message = appendLine(info.Message, "Compilation exceeded the memory limit.")
		case sandbox.StatusOutputLimit:
			info.Message = appendLine(info.Message, "Compilation produced a file that is too large.")
		}
		return info, "", nil
	}

	artifactPath := filepath.Join(workDir, language.Artifact)
	if err := copyFile(filepath.Join(box.Dir(), language.Artifact), artifactPath, os.FileMode(language.ArtifactMode)); err != nil {
		return contract.CompileInfo{}, "", systemError(CodeSandboxFailure, "collect compiled program: %v", err)
	}
	return info, artifactPath, nil
}

func (p *Pipeline) runTest(ctx context.Context, language runner.Language, artifactPath string, limits contract.Limits,
	test contract.TestCase, inputPath, expectedPath string, checker comparator.Checker) (contract.TestResult, error) {
	box, err := p.sandbox.Acquire(ctx)
	if err != nil {
		return contract.TestResult{}, systemError(CodeSandboxFailure, "acquire box for test %s: %v", test.Id, err)
	}
	defer box.Cleanup()

	if err := copyFile(artifactPath, filepath.Join(box.Dir(), language.Artifact), os.FileMode(language.ArtifactMode)); err != nil {
		return contract.TestResult{}, systemError(CodeSandboxFailure, "copy program for test %s: %v", test.Id, err)
	}
	if err := copyFile(inputPath, filepath.Join(box.Dir(), inputFile), 0o644); err != nil {
		return contract.TestResult{}, systemError(CodeSandboxFailure, "copy input for test %s: %v", test.Id, err)
	}

	outputKB := defaultOutputKB
	if limits.OutputKb != nil {
		outputKB = *limits.OutputKb
	}
	cpuTime := time.Duration(limits.TimeMs) * time.Millisecond
	run, err := box.Run(ctx, sandbox.RunRequest{
		Args:   language.RunArgs,
		Stdin:  inputFile,
		Stdout: outputFile,
		Stderr: stderrFile,
		Env:    []string{"PATH=" + p.config.Path, "HOME=/box", "LANG=C.UTF-8"},
		Limits: sandbox.Limits{
			CPUTime:    cpuTime,
			WallTime:   cpuTime*time.Duration(p.config.WallTimeFactor) + time.Second,
			ExtraTime:  min(p.config.ExtraTime, cpuTime),
			MemoryKB:   limits.MemoryKb,
			Processes:  language.RunProcesses,
			FileSizeKB: outputKB,
			StackKB:    limits.MemoryKb,
			OpenFiles:  p.config.OpenFiles,
		},
	})
	if err != nil {
		return contract.TestResult{}, systemError(CodeSandboxFailure, "run test %s: %v", test.Id, err)
	}

	result := contract.TestResult{
		Id:       test.Id,
		TimeMs:   int(run.CPUTime.Milliseconds()),
		MemoryKb: run.MaxRSSKB,
	}
	isSample := test.Sample != nil && *test.Sample
	outputPath := filepath.Join(box.Dir(), outputFile)

	status := run.Status
	// Output that reached the size limit is OLE whatever happened next: C
	// programs die on SIGXFSZ, but Python ignores that signal and exits
	// with an error once the write fails.
	if status == sandbox.StatusRuntimeError || status == sandbox.StatusSignaled || status == sandbox.StatusOK {
		if info, err := os.Stat(outputPath); err == nil && info.Size() >= int64(outputKB)*1024 {
			status = sandbox.StatusOutputLimit
		}
	}

	switch status {
	case sandbox.StatusTimeLimit:
		result.Verdict = contract.TestVerdictTLE
	case sandbox.StatusMemoryLimit:
		result.Verdict = contract.TestVerdictMLE
	case sandbox.StatusOutputLimit:
		result.Verdict = contract.TestVerdictOLE
	case sandbox.StatusRuntimeError:
		result.Verdict = contract.TestVerdictRE
		result.ExitCode = &run.ExitCode
	case sandbox.StatusSignaled:
		result.Verdict = contract.TestVerdictRE
		result.Signal = &run.Signal
	case sandbox.StatusOK:
		verdict, message, err := compareOutput(checker, expectedPath, outputPath)
		if err != nil {
			return contract.TestResult{}, systemError(CodeSandboxFailure, "compare test %s: %v", test.Id, err)
		}
		result.Verdict = verdict
		if isSample && message != "" {
			trimmed := runner.TruncateUTF8(message, checkerMessageMax)
			result.CheckerMessage = &trimmed
		}
	default:
		return contract.TestResult{}, systemError(CodeSandboxFailure, "test %s: sandbox status %s", test.Id, status)
	}

	// Only sample tests may echo output back: hidden test content (which a
	// program could copy from its input to its output) never leaves Judge.
	if isSample {
		preview, _ := readPrefix(outputPath, stdoutPreviewBytes)
		text := runner.TruncateUTF8(strings.ToValidUTF8(string(preview), "\uFFFD"), stdoutPreviewBytes)
		result.StdoutPreview = &text
	}
	return result, nil
}

func compareOutput(checker comparator.Checker, expectedPath, outputPath string) (contract.TestVerdict, string, error) {
	expectedFile, err := os.Open(expectedPath)
	if err != nil {
		return "", "", err
	}
	defer expectedFile.Close()
	var actual io.Reader = eofReader{}
	if outputFile, err := os.Open(outputPath); err == nil {
		defer outputFile.Close()
		actual = outputFile
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	check, err := checker.Check(expectedFile, actual)
	if err != nil {
		return "", "", err
	}
	if check.Accepted {
		return contract.TestVerdictAC, "", nil
	}
	return contract.TestVerdictWA, check.Message, nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	// OpenFile's mode is filtered by the umask; set it exactly.
	return os.Chmod(destination, mode)
}

// readPrefix reads at most limit bytes from the start of a file. A missing
// file reads as empty.
func readPrefix(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func appendLine(text, line string) string {
	if text == "" {
		return line
	}
	return text + "\n" + line
}
