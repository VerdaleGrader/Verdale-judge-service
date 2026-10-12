//go:build integration

// Package integration runs the judging pipeline against the real isolate
// sandbox and real compilers. It needs Linux with cgroup v2 and root:
// run it with `make test-integration` (a privileged container).
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/configs"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/runner"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
	"github.com/kanonkc/verdale-judge-service/pkg/testfiles"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
)

const problemDir = "../examples/aplusb"

func newPipeline(t *testing.T) (*pipeline.Pipeline, []contract.TestCase) {
	t.Helper()
	// Secrets are irrelevant here; the file supplies the pinned toolchains.
	cfg := mustReadLanguages(t)

	isolate, err := sandbox.NewIsolate(sandbox.IsolateConfig{
		IsolatePath: "/usr/local/bin/isolate", CgroupRoot: "/sys/fs/cgroup", BoxIDBase: 500, BoxCount: 2, MetaDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NoError(t, isolate.CheckEnvironment(context.Background()), "cgroup v2 and isolate must be ready")
	languages := runner.Languages(cfg.Languages)
	require.NoError(t, runner.CheckVersions(context.Background(), languages, sandbox.ExecRunner), "toolchains match the pinned versions")

	storageDir := t.TempDir()
	cache, err := testfiles.OpenDiskCache(t.TempDir(), 1<<30)
	require.NoError(t, err)
	fetcher := testfiles.NewFetcher(testfiles.LocalStore{Dir: storageDir}, cache, testfiles.FetcherConfig{
		KeyPrefix: "tests/", MaxFileBytes: 1 << 28, Attempts: 2, Backoff: 10 * time.Millisecond,
	})

	var tests []contract.TestCase
	for _, id := range []string{"1", "2", "3", "4"} {
		input := store(t, storageDir, filepath.Join(problemDir, "tests", id+".in"))
		output := store(t, storageDir, filepath.Join(problemDir, "tests", id+".out"))
		sample := id == "1"
		tests = append(tests, contract.TestCase{Id: id, Input: input, Output: &output, Sample: &sample})
	}

	compile := cfg.Sandbox.Compile
	return pipeline.New(isolate, fetcher, languages, pipeline.Config{
		WorkDir: t.TempDir(), Path: cfg.Sandbox.Path, ExtraTime: 500 * time.Millisecond, WallTimeFactor: 3, OpenFiles: 64,
		Compile: pipeline.CompileLimits{
			CPUTime: time.Duration(compile.TimeMs) * time.Millisecond, WallTime: time.Duration(compile.WallTimeMs) * time.Millisecond,
			MemoryKB: compile.MemoryKb, Processes: compile.Processes, OutputKB: compile.OutputKb, MessageLimitBytes: compile.MessageLimitBytes,
		},
		JudgeVersion: "integration",
	}), tests
}

func mustReadLanguages(t *testing.T) *configs.Configuration {
	t.Setenv("VERDALE_JUDGE_AUTH_INBOUND_KEYS", "test:0123456789abcdef0123456789abcdef")
	t.Setenv("VERDALE_JUDGE_CALLBACK_KEY_ID", "test")
	t.Setenv("VERDALE_JUDGE_CALLBACK_SECRET", "0123456789abcdef0123456789abcdef")
	cfg, err := configs.Load("../config.json")
	require.NoError(t, err)
	return cfg
}

func store(t *testing.T, storageDir, path string) contract.TestFile {
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	require.NoError(t, os.MkdirAll(filepath.Join(storageDir, "tests"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(storageDir, "tests", digest), content, 0o644))
	return contract.TestFile{Sha256: digest, SizeBytes: int64(len(content))}
}

func judge(t *testing.T, p *pipeline.Pipeline, tests []contract.TestCase, solution string, kind contract.JobKind) contract.JobResult {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(problemDir, "solutions", solution))
	require.NoError(t, err)
	language := map[string]contract.Language{".c": contract.LanguageC, ".cpp": contract.LanguageCpp, ".py": contract.LanguagePython}[filepath.Ext(solution)]
	outputKB := 1024
	result, err := p.Execute(context.Background(), contract.JobRequest{
		JobId: uuid.New(), Kind: kind, Language: language, Source: string(source),
		Limits: contract.Limits{TimeMs: 1000, MemoryKb: 65536, OutputKb: &outputKB}, Tests: tests, CallbackUrl: "http://core/",
	})
	require.NoError(t, err, "no system error for %s", solution)
	return result
}

func TestVerdictsForEverySolution(t *testing.T) {
	p, tests := newPipeline(t)
	expected := map[string]contract.Verdict{
		"ac.c": contract.VerdictAC, "ac.cpp": contract.VerdictAC, "ac.py": contract.VerdictAC,
		"wa.cpp": contract.VerdictWA,
		"tle.c":  contract.VerdictTLE, "tle.py": contract.VerdictTLE,
		"mle.cpp": contract.VerdictMLE, "mle.py": contract.VerdictMLE,
		"re.c": contract.VerdictRE, "re.py": contract.VerdictRE,
		"ce.c": contract.VerdictCE, "ce.cpp": contract.VerdictCE, "ce.py": contract.VerdictCE,
		"ole.py":    contract.VerdictOLE,
		"escape.py": contract.VerdictWA,
	}
	for solution, verdict := range expected {
		t.Run(solution, func(t *testing.T) {
			result := judge(t, p, tests, solution, contract.JobKindSubmission)
			assert.Equal(t, verdict, result.Verdict, "compile: %+v tests: %+v", result.Compile, result.Tests)
			if verdict == contract.VerdictTLE {
				assert.GreaterOrEqual(t, result.Tests[0].TimeMs, 1000)
			}
		})
	}
}

func TestCompileErrorMessages(t *testing.T) {
	p, tests := newPipeline(t)

	result := judge(t, p, tests, "ce.cpp", contract.JobKindSubmission)
	assert.Contains(t, result.Compile.Message, "undeclared_variable")
	assert.NotContains(t, result.Compile.Message, "/var/local/lib/isolate", "no host paths")

	result = judge(t, p, tests, "ce.py", contract.JobKindSubmission)
	assert.Contains(t, result.Compile.Message, "SyntaxError", "a Python syntax error is reported as CE with the message")
	assert.Empty(t, result.Tests)
}

func TestSandboxBlocksEscapes(t *testing.T) {
	p, tests := newPipeline(t)
	result := judge(t, p, tests, "escape.py", contract.JobKindSubmission)
	require.NotNil(t, result.Tests[0].StdoutPreview)
	report := *result.Tests[0].StdoutPreview
	t.Log("\n" + report)
	assert.NotContains(t, report, "ALLOWED", "every escape attempt must be blocked")
	assert.Contains(t, report, "network: blocked")
	assert.Contains(t, report, "processes visible: 1", "only its own process")
}

func TestVerifyRunsEveryTestAndSubmissionStops(t *testing.T) {
	p, tests := newPipeline(t)
	submission := judge(t, p, tests, "wa.cpp", contract.JobKindSubmission)
	assert.Equal(t, []contract.TestVerdict{contract.TestVerdictAC, contract.TestVerdictWA, contract.TestVerdictSKIPPED, contract.TestVerdictSKIPPED}, verdictsOf(submission))

	verify := judge(t, p, tests, "wa.cpp", contract.JobKindVerify)
	assert.NotContains(t, verdictsOf(verify), contract.TestVerdictSKIPPED)
	assert.Equal(t, 4, len(verify.Tests))

	accepted := judge(t, p, tests, "ac.cpp", contract.JobKindVerify)
	assert.Equal(t, contract.VerdictAC, accepted.Verdict)
	assert.Equal(t, 4, accepted.Summary.Passed)
	for _, test := range accepted.Tests {
		assert.Greater(t, test.MemoryKb, 0, "memory is measured for test %s", test.Id)
	}
	assert.Nil(t, accepted.Tests[1].StdoutPreview, "hidden test output never leaves Judge")
}

func verdictsOf(result contract.JobResult) []contract.TestVerdict {
	var verdicts []contract.TestVerdict
	for _, test := range result.Tests {
		verdicts = append(verdicts, test.Verdict)
	}
	return verdicts
}

func TestCaptureAndGenerateWithTheRealSandbox(t *testing.T) {
	p, tests := newPipeline(t)
	source, err := os.ReadFile(filepath.Join(problemDir, "solutions", "ac.py"))
	require.NoError(t, err)
	stdout, stderr := 4096, 1024
	run := func(kind contract.JobKind, withOutputs bool) contract.JobResult {
		cases := append([]contract.TestCase(nil), tests...)
		if !withOutputs {
			for index := range cases {
				cases[index].Output = nil
			}
		}
		result, err := p.Execute(context.Background(), contract.JobRequest{
			JobId: uuid.New(), Kind: kind, Language: contract.LanguagePython, Source: string(source),
			Limits:  contract.Limits{TimeMs: 1000, MemoryKb: 65536},
			Capture: &contract.Capture{StdoutBytes: &stdout, StderrBytes: &stderr},
			Tests:   cases, CallbackUrl: "http://core/",
		})
		require.NoError(t, err)
		return result
	}

	// A normal job gets the output of hidden tests too, once Core asks.
	verified := run(contract.JobKindVerify, true)
	require.Equal(t, contract.VerdictAC, verified.Verdict)
	for _, test := range verified.Tests {
		require.NotNil(t, test.Stdout, "test %s", test.Id)
		require.NotNil(t, test.StdoutSizeBytes)
	}

	// A generate job needs no expected outputs and returns what the program printed.
	generated := run(contract.JobKindGenerate, false)
	require.Equal(t, contract.VerdictAC, generated.Verdict)
	for index, test := range generated.Tests {
		assert.Equal(t, *verified.Tests[index].Stdout, *test.Stdout, "test %s", test.Id)
	}
}
