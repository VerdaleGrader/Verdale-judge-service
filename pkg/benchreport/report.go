// Package benchreport holds the timing benchmark's report (GRD-42): what was
// measured, on which machine and runtime, and how to compare two reports.
package benchreport

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// Machine records what the numbers depend on, so a change of machine or
// runtime can be told apart from a change of the code.
type Machine struct {
	Hostname   string   `json:"hostname"`
	CPUModel   string   `json:"cpu_model"`
	NumCPU     int      `json:"num_cpu"`
	Kernel     string   `json:"kernel"`
	Isolate    string   `json:"isolate"`
	Toolchains []string `json:"toolchains"`
	PinnedCPUs []int    `json:"pinned_cpus"`
}

// Stats describes one set of samples of the time a program was measured at.
type Stats struct {
	Samples  int     `json:"samples"`
	MinMs    int     `json:"min_ms"`
	MedianMs int     `json:"median_ms"`
	P95Ms    int     `json:"p95_ms"`
	MaxMs    int     `json:"max_ms"`
	MeanMs   float64 `json:"mean_ms"`
	StdDevMs float64 `json:"stddev_ms"`
	// SpreadPct is how far the 95th percentile is above the median.
	SpreadPct float64 `json:"spread_pct"`
}

// Program is the result for one benchmark program.
type Program struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	// Alone is one job at a time; Together is `parallel_jobs` jobs at once.
	Alone    Stats `json:"alone"`
	Together Stats `json:"together"`
	// SlowdownPct is how much slower the median is with company.
	SlowdownPct float64 `json:"slowdown_pct"`
}

type Report struct {
	Label        string    `json:"label"`
	TakenAt      string    `json:"taken_at"`
	Machine      Machine   `json:"machine"`
	Rounds       int       `json:"rounds"`
	ParallelJobs int       `json:"parallel_jobs"`
	Programs     []Program `json:"programs"`
}

// Summarize computes the statistics of a set of times in milliseconds.
func Summarize(times []int) Stats {
	if len(times) == 0 {
		return Stats{}
	}
	sorted := append([]int(nil), times...)
	sort.Ints(sorted)
	var sum float64
	for _, value := range sorted {
		sum += float64(value)
	}
	mean := sum / float64(len(sorted))
	var squares float64
	for _, value := range sorted {
		squares += (float64(value) - mean) * (float64(value) - mean)
	}
	stats := Stats{
		Samples: len(sorted), MinMs: sorted[0], MedianMs: sorted[len(sorted)/2], P95Ms: percentile(sorted, 95),
		MaxMs: sorted[len(sorted)-1], MeanMs: round1(mean), StdDevMs: round1(math.Sqrt(squares / float64(len(sorted)))),
	}
	if stats.MedianMs > 0 {
		stats.SpreadPct = round1(float64(stats.P95Ms-stats.MedianMs) / float64(stats.MedianMs) * 100)
	}
	return stats
}

// percentile is the nearest-rank percentile of a sorted slice.
func percentile(sorted []int, percent int) int {
	rank := int(math.Ceil(float64(percent) / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

func round1(value float64) float64 { return math.Round(value*10) / 10 }

// NewProgram builds a Program from its two sets of samples.
func NewProgram(name, language string, alone, together []int) Program {
	program := Program{Name: name, Language: language, Alone: Summarize(alone), Together: Summarize(together)}
	if program.Alone.MedianMs > 0 {
		program.SlowdownPct = round1(float64(program.Together.MedianMs-program.Alone.MedianMs) / float64(program.Alone.MedianMs) * 100)
	}
	return program
}

// Markdown renders the report as a table for docs and logs.
func (r Report) Markdown() string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s on %s (%d CPUs, %s), %d rounds, %d jobs together\n\n", r.Label, r.Machine.CPUModel, r.Machine.NumCPU, r.Machine.Kernel, r.Rounds, r.ParallelJobs)
	out.WriteString("| program | alone median ms | alone p95 ms | spread % | together median ms | slowdown % |\n|---|---:|---:|---:|---:|---:|\n")
	for _, program := range r.Programs {
		fmt.Fprintf(&out, "| %s | %d | %d | %.1f | %d | %.1f |\n", program.Name, program.Alone.MedianMs, program.Alone.P95Ms,
			program.Alone.SpreadPct, program.Together.MedianMs, program.SlowdownPct)
	}
	return out.String()
}

func Load(path string) (Report, error) {
	var report Report
	raw, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	return report, json.Unmarshal(raw, &report)
}

func Save(path string, report Report) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Difference is how one program's median moved between two reports.
type Difference struct {
	Name      string
	BeforeMs  int
	AfterMs   int
	ChangePct float64
	Exceeds   bool
}

// Compare lists, per program present in both reports, how much the median
// time alone changed. A change beyond thresholdPct is flagged: the machine,
// the kernel or a toolchain moved the numbers, and time limits that were set
// with the old numbers need a second look.
func Compare(before, after Report, thresholdPct float64) []Difference {
	earlier := map[string]Program{}
	for _, program := range before.Programs {
		earlier[program.Name] = program
	}
	var differences []Difference
	for _, program := range after.Programs {
		old, found := earlier[program.Name]
		if !found || old.Alone.MedianMs == 0 {
			continue
		}
		change := round1(float64(program.Alone.MedianMs-old.Alone.MedianMs) / float64(old.Alone.MedianMs) * 100)
		differences = append(differences, Difference{
			Name: program.Name, BeforeMs: old.Alone.MedianMs, AfterMs: program.Alone.MedianMs,
			ChangePct: change, Exceeds: math.Abs(change) > thresholdPct,
		})
	}
	return differences
}
