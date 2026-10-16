//go:build integration && benchmark

// The timing benchmark (GRD-42): how steady is the time Judge measures?
//
//	make benchmark                       in the test container
//	make benchmark-host                  on the Judge machine itself (as root)
//
// Settings (environment): BENCHMARK_ROUNDS (default 10), BENCHMARK_PARALLEL
// (jobs at once, default 4), INTEGRATION_BOX_BASE and INTEGRATION_CPUS (the
// boxes and cores isolate is configured with; see sandbox_test.go), BENCHMARK_CPUS (cores to pin to, e.g. "2-5"),
// BENCHMARK_MAX_SPREAD_PCT (fail above this, default 25), BENCHMARK_LABEL and
// BENCHMARK_OUT (where the report goes, default ../docs/benchmarks).
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/benchreport"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

// spreadFloorMs is the least spread that is tolerated whatever the percentage.
const spreadFloorMs = 5

type benchmarkProgram struct {
	file     string
	language contract.Language
}

var benchmarkPrograms = []benchmarkProgram{
	{"cpu.c", contract.LanguageC},
	{"sort.cpp", contract.LanguageCpp},
	{"memory.c", contract.LanguageC},
	{"output.c", contract.LanguageC},
	{"interpreter.py", contract.LanguagePython},
}

func TestTimingBenchmark(t *testing.T) {
	rounds := environmentNumber("BENCHMARK_ROUNDS", 10)
	parallel := environmentNumber("BENCHMARK_PARALLEL", parallelBoxes())
	maxSpread := float64(environmentNumber("BENCHMARK_MAX_SPREAD_PCT", 25))
	cpus := firstCores(parallel)

	p, tests := newPipelineWith(t, pipelineOptions{boxes: parallel, cpus: cpus})
	limits := contract.Limits{TimeMs: 10000, MemoryKb: 524288}

	report := benchreport.Report{
		Label: environmentText("BENCHMARK_LABEL", hostname()), TakenAt: time.Now().UTC().Format(time.RFC3339),
		Machine: describeMachine(cpus), Rounds: rounds, ParallelJobs: parallel,
	}
	for _, program := range benchmarkPrograms {
		source, err := os.ReadFile(filepath.Join(benchmarkDir, program.file))
		require.NoError(t, err)

		// One warm-up job so file caches and the CPU clock are not what the first sample measures.
		judgeAll(t, p, tests, program.language, string(source), 1, limits)

		var alone, together []int
		for round := 0; round < rounds; round++ {
			alone = append(alone, timesOf(judgeAll(t, p, tests, program.language, string(source), 1, limits))...)
			together = append(together, timesOf(judgeAll(t, p, tests, program.language, string(source), parallel, limits))...)
		}
		result := benchreport.NewProgram(program.file, string(program.language), alone, together)
		report.Programs = append(report.Programs, result)

		// A millisecond is the resolution, so a few milliseconds of spread is never a finding.
		for name, stats := range map[string]benchreport.Stats{"alone": result.Alone, "together": result.Together} {
			allowed := max(float64(stats.MedianMs)*maxSpread/100, spreadFloorMs)
			assert.LessOrEqual(t, float64(stats.P95Ms-stats.MedianMs), allowed,
				"%s %s: the 95th percentile (%d ms) is too far above the median (%d ms)", program.file, name, stats.P95Ms, stats.MedianMs)
		}
	}

	t.Log("\n" + report.Markdown())
	out := environmentText("BENCHMARK_OUT", "../docs/benchmarks")
	require.NoError(t, os.MkdirAll(out, 0o755))
	path := filepath.Join(out, fmt.Sprintf("%s-%s.json", time.Now().UTC().Format("20060102-150405"), report.Label))
	require.NoError(t, benchreport.Save(path, report))
	t.Logf("report saved to %s", path)
}

func describeMachine(cpus []int) benchreport.Machine {
	machine := benchreport.Machine{Hostname: hostname(), NumCPU: runtime.NumCPU(), PinnedCPUs: cpus}
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if name, found := strings.CutPrefix(line, "model name"); found {
				machine.CPUModel = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), ":"))
				break
			}
		}
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		machine.Kernel = strings.TrimSpace(string(raw))
	}
	machine.Isolate = commandLine("/usr/local/bin/isolate", "--version")
	for _, command := range [][]string{{"gcc", "--version"}, {"g++", "--version"}, {"python3", "--version"}} {
		machine.Toolchains = append(machine.Toolchains, commandLine(command[0], command[1:]...))
	}
	return machine
}

// commandLine is the first line a command prints, or "unknown".
func commandLine(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return first
}
