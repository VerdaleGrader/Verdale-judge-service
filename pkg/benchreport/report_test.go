package benchreport

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarize(t *testing.T) {
	stats := Summarize([]int{100, 102, 98, 101, 99, 100, 103, 97, 100, 130})
	assert.Equal(t, 10, stats.Samples)
	assert.Equal(t, 97, stats.MinMs)
	assert.Equal(t, 100, stats.MedianMs)
	assert.Equal(t, 130, stats.P95Ms, "nearest rank: the 10th of 10 values")
	assert.Equal(t, 130, stats.MaxMs)
	assert.InDelta(t, 103.0, stats.MeanMs, 0.01)
	assert.InDelta(t, 30.0, stats.SpreadPct, 0.01)

	assert.Equal(t, Stats{}, Summarize(nil))
	assert.Equal(t, 5, Summarize([]int{5}).MedianMs)
}

func TestProgramSlowdownAndCompare(t *testing.T) {
	program := NewProgram("cpu.c", "c", []int{100, 100, 100}, []int{110, 110, 110})
	assert.InDelta(t, 10.0, program.SlowdownPct, 0.01)

	before := Report{Programs: []Program{program, NewProgram("sort.cpp", "cpp", []int{200}, []int{200})}}
	after := Report{Programs: []Program{NewProgram("cpu.c", "c", []int{125}, []int{125}), NewProgram("sort.cpp", "cpp", []int{204}, []int{204}), NewProgram("new.c", "c", []int{1}, []int{1})}}
	differences := Compare(before, after, 10)
	require.Len(t, differences, 2, "a program missing from the old report is skipped")
	assert.Equal(t, "cpu.c", differences[0].Name)
	assert.InDelta(t, 25.0, differences[0].ChangePct, 0.01)
	assert.True(t, differences[0].Exceeds)
	assert.False(t, differences[1].Exceeds, "2 %% is inside the threshold")
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	report := Report{Label: "judge-1", Rounds: 3, Programs: []Program{NewProgram("cpu.c", "c", []int{1, 2, 3}, []int{2, 3, 4})}}
	require.NoError(t, Save(path, report))
	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, report, loaded)
	assert.Contains(t, report.Markdown(), "| cpu.c |")
}
