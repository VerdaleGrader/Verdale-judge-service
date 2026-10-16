//go:build integration

// Parallel judging (GRD-40): every running job has a box of its own that is
// locked to a core of its own, and running together does not change the time
// a program is measured at.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
)

// parallelBoxes is how many jobs run together: four, or every core there is.
func parallelBoxes() int { return max(2, min(4, runtime.NumCPU())) }

// firstCores is the cores the boxes are locked to: INTEGRATION_CPUS, or core 0, 1, ...
func firstCores(count int) []int {
	if list := os.Getenv("INTEGRATION_CPUS"); list != "" {
		cores, err := sandbox.ParseCPUList(list)
		if err != nil || len(cores) < count {
			panic("INTEGRATION_CPUS needs a core for each of the " + strconv.Itoa(count) + " parallel jobs: " + list)
		}
		return cores
	}
	cores := make([]int, count)
	for index := range cores {
		cores[index] = index % runtime.NumCPU()
	}
	return cores
}

// judgeAll runs the same source as `jobs` jobs at once and returns every result.
func judgeAll(t *testing.T, p *pipeline.Pipeline, tests []contract.TestCase, language contract.Language, source string, jobs int, limits contract.Limits) []contract.JobResult {
	t.Helper()
	stdoutBytes := 4096
	results := make([]contract.JobResult, jobs)
	var group sync.WaitGroup
	for index := 0; index < jobs; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			result, err := p.Execute(ctx, contract.JobRequest{
				JobId: uuid.New(), Kind: contract.JobKindVerify, Language: language, Source: source, Limits: limits,
				Capture: &contract.Capture{StdoutBytes: &stdoutBytes}, Tests: tests, CallbackUrl: "http://core/",
			})
			assert.NoError(t, err)
			results[index] = result
		}()
	}
	group.Wait()
	return results
}

func TestParallelJobsGetBoxesLockedToDistinctCores(t *testing.T) {
	boxes := parallelBoxes()
	p, tests := newPipelineWith(t, pipelineOptions{boxes: boxes, cpus: firstCores(boxes)})
	tests = tests[:1]

	// Each job reports the cores it may run on, then waits so that all of them overlap.
	source := `import time
allowed = [line for line in open("/proc/self/status") if line.startswith("Cpus_allowed_list")][0]
print(allowed.split(":")[1].strip())
time.sleep(0.6)
`
	results := judgeAll(t, p, tests, contract.LanguagePython, source, boxes, contract.Limits{TimeMs: 1000, MemoryKb: 65536})

	var cores []string
	for _, result := range results {
		require.Len(t, result.Tests, 1)
		require.NotNil(t, result.Tests[0].Stdout, "tests: %+v", result.Tests)
		cores = append(cores, strings.TrimSpace(*result.Tests[0].Stdout))
	}
	sort.Strings(cores)
	t.Logf("cores seen by %d parallel jobs: %v", boxes, cores)
	for _, core := range cores {
		assert.NotContains(t, core, "-", "a box may run on one core only, got %q", core)
		assert.NotContains(t, core, ",", "a box may run on one core only, got %q", core)
	}
	assert.Len(t, uniqueStrings(cores), boxes, "jobs running together must not share a core")
	assertNothingLeftBehind(t)
}

func TestParallelRunsDoNotChangeMeasuredTime(t *testing.T) {
	boxes := parallelBoxes()
	p, tests := newPipelineWith(t, pipelineOptions{boxes: boxes, cpus: firstCores(boxes)})
	source, err := os.ReadFile(filepath.Join(benchmarkDir, "cpu.c"))
	require.NoError(t, err)
	limits := contract.Limits{TimeMs: 10000, MemoryKb: 262144}

	solo := timesOf(judgeAll(t, p, tests, contract.LanguageC, string(source), 1, limits))
	together := timesOf(judgeAll(t, p, tests, contract.LanguageC, string(source), boxes, limits))
	soloMedian, togetherMedian := median(solo), median(together)
	t.Logf("alone: median %d ms of %v; with %d running together: median %d ms of %v", soloMedian, solo, boxes, togetherMedian, together)

	require.Greater(t, soloMedian, 100, "the program must run long enough to measure")
	// Docker on a laptop is a noisy place; on a Judge machine the benchmark (GRD-42) shows the real spread.
	// The median, not the worst run: a laptop VM has outliers, which the benchmark reports.
	assert.LessOrEqual(t, togetherMedian, soloMedian*13/10+20, "running together may not make a program more than 30%% slower")
}

func timesOf(results []contract.JobResult) []int {
	var times []int
	for _, result := range results {
		for _, test := range result.Tests {
			times = append(times, test.TimeMs)
		}
	}
	return times
}

func median(values []int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

func uniqueStrings(values []string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	return set
}

const benchmarkDir = "../examples/benchmarks"
