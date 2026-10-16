//go:build integration

// The attack suite (GRD-57): programs that try to hurt the host or Judge. A
// test passes when the verdict is a safe one, nothing of the host leaks into
// the result, and no process, box or control group is left behind.
package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
)

const (
	attackDir      = "../examples/attacks"
	isolateBoxRoot = "/var/local/lib/isolate"
	isolateCgroup  = "/sys/fs/cgroup/isolate"
	// first_uid in deploy/docker/isolate.cf: processes of sandboxes run as uids from here.
	firstSandboxUID = 60000
)

// hostCanary is a file of the host with a secret in it. No attack may read,
// change or return it.
type hostCanary struct {
	path   string
	secret string
}

func newHostCanary(t *testing.T) hostCanary {
	t.Helper()
	canary := hostCanary{path: filepath.Join(t.TempDir(), "canary.txt"), secret: "CANARY-" + uuid.NewString()}
	require.NoError(t, os.WriteFile(canary.path, []byte(canary.secret), 0o644))
	return canary
}

func (c hostCanary) assertUntouched(t *testing.T) {
	t.Helper()
	content, err := os.ReadFile(c.path)
	require.NoError(t, err)
	assert.Equal(t, c.secret, string(content), "the host file was changed")
}

type attack struct {
	file string
	// safe lists the verdicts that count as the sandbox holding. AC is never one of them.
	safe []contract.Verdict
	// within is how long Judge may take, including clean-up.
	within time.Duration
	// stdoutHas / stdoutLacks are checked on the program's captured output.
	stdoutHas, stdoutLacks []string
}

func TestAttacksEndWithASafeVerdictAndNothingLeft(t *testing.T) {
	p, tests := newPipeline(t)
	canary := newHostCanary(t)
	attacks := []attack{
		{file: "forkbomb.c", safe: verdicts(contract.VerdictTLE, contract.VerdictRE, contract.VerdictMLE), within: 20 * time.Second},
		{file: "threadbomb.py", safe: verdicts(contract.VerdictWA), within: 20 * time.Second, stdoutHas: []string{"threads started: 0"}},
		{file: "membomb.c", safe: verdicts(contract.VerdictMLE), within: 20 * time.Second},
		{file: "stackbomb.c", safe: verdicts(contract.VerdictRE, contract.VerdictMLE), within: 20 * time.Second},
		{file: "loop.c", safe: verdicts(contract.VerdictTLE), within: 20 * time.Second},
		{file: "sleeper.py", safe: verdicts(contract.VerdictTLE), within: 20 * time.Second},
		{file: "bigoutput.c", safe: verdicts(contract.VerdictOLE), within: 20 * time.Second},
		{file: "diskfill.c", safe: verdicts(contract.VerdictTLE, contract.VerdictOLE, contract.VerdictRE, contract.VerdictMLE), within: 20 * time.Second},
		{file: "openfiles.py", safe: verdicts(contract.VerdictWA), within: 20 * time.Second, stdoutHas: []string{"open files:"}},
		{file: "symlink_output.py", safe: verdicts(contract.VerdictWA, contract.VerdictRE), within: 20 * time.Second},
		{file: "symlink_stderr.py", safe: verdicts(contract.VerdictWA, contract.VerdictRE), within: 20 * time.Second},
		{file: "fifo_output.py", safe: verdicts(contract.VerdictWA, contract.VerdictRE), within: 20 * time.Second},
		{file: "probe_host.py", safe: verdicts(contract.VerdictWA), within: 20 * time.Second,
			stdoutHas: []string{"tcp internet: blocked", "uid:", "root: blocked", "processes visible: 1"}, stdoutLacks: []string{"ALLOWED"}},
	}
	for _, testCase := range attacks {
		t.Run(testCase.file, func(t *testing.T) {
			result, text := runAttack(t, p, tests, testCase, canary)

			t.Logf("verdict %s, tests %s", result.Verdict, summarize(result))
			assert.NotEqual(t, contract.VerdictAC, result.Verdict, "an attack must never be accepted")
			assert.Contains(t, testCase.safe, result.Verdict, "tests: %+v", result.Tests)
			assert.NotContains(t, text, canary.secret, "the content of a host file reached the result")
			for _, wanted := range testCase.stdoutHas {
				assert.Contains(t, text, wanted)
			}
			for _, unwanted := range testCase.stdoutLacks {
				assert.NotContains(t, text, unwanted)
			}
			canary.assertUntouched(t)
			assertNothingLeftBehind(t)
		})
	}
}

func TestOpenFilesStayWithinTheLimit(t *testing.T) {
	p, tests := newPipeline(t)
	result, text := runAttack(t, p, tests, attack{file: "openfiles.py", within: 20 * time.Second}, newHostCanary(t))
	require.NotEmpty(t, result.Tests)
	line := text[strings.Index(text, "open files: "):]
	fields := strings.Fields(line)
	require.GreaterOrEqual(t, len(fields), 3)
	opened, err := strconv.Atoi(fields[2])
	require.NoError(t, err)
	assert.LessOrEqual(t, opened, 64, "sandbox.open_files is 64 in config.json")
}

// runAttack judges one attack source against a sample test (so a leak by the
// preview would show) and asks for the captured output (so a leak by the
// capture would show). It returns the result and its JSON.
func runAttack(t *testing.T, p *pipeline.Pipeline, tests []contract.TestCase, testCase attack, canary hostCanary) (contract.JobResult, string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(attackDir, testCase.file))
	require.NoError(t, err)
	code := strings.ReplaceAll(string(source), "__CANARY__", canary.path)
	language := map[string]contract.Language{".c": contract.LanguageC, ".cpp": contract.LanguageCpp, ".py": contract.LanguagePython}[filepath.Ext(testCase.file)]
	outputKB, stdoutBytes, stderrBytes := 1024, 4096, 1024

	ctx, cancel := context.WithTimeout(context.Background(), testCase.within)
	defer cancel()
	started := time.Now()
	result, err := p.Execute(ctx, contract.JobRequest{
		JobId: uuid.New(), Kind: contract.JobKindSubmission, Language: language, Source: code,
		Limits:  contract.Limits{TimeMs: 1000, MemoryKb: 65536, OutputKb: &outputKB},
		Capture: &contract.Capture{StdoutBytes: &stdoutBytes, StderrBytes: &stderrBytes},
		Tests:   tests, CallbackUrl: "http://core/",
	})
	require.NoError(t, err, "an attack must not turn into a system error (%s took %s)", testCase.file, time.Since(started))
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	return result, string(encoded)
}

// assertNothingLeftBehind checks that the box was removed, its control group
// is gone and no process of a sandbox user is still running.
func assertNothingLeftBehind(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return len(sandboxProcesses()) == 0 }, 5*time.Second, 100*time.Millisecond,
		"processes of the sandbox are still running: %v", sandboxProcesses())

	boxes, err := os.ReadDir(isolateBoxRoot)
	require.NoError(t, err)
	assert.Empty(t, boxes, "box directories are left in %s", isolateBoxRoot)

	groups, err := filepath.Glob(filepath.Join(isolateCgroup, "box-*"))
	require.NoError(t, err)
	assert.Empty(t, groups, "control groups are left in %s", isolateCgroup)
}

// sandboxProcesses lists the pids whose real uid belongs to a sandbox.
func sandboxProcesses() []string {
	entries, _ := os.ReadDir("/proc")
	var found []string
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					if uid, _ := strconv.Atoi(fields[0]); uid >= firstSandboxUID {
						found = append(found, entry.Name())
					}
				}
			}
		}
	}
	return found
}

func verdicts(list ...contract.Verdict) []contract.Verdict { return list }

func summarize(result contract.JobResult) string {
	var parts []string
	for _, test := range result.Tests {
		part := test.Id + "=" + string(test.Verdict)
		if test.Stdout != nil {
			part += " stdout=" + strconv.Quote(*test.Stdout)
		}
		if test.Stderr != nil && *test.Stderr != "" {
			part += " stderr=" + strconv.Quote(*test.Stderr)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
