package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPUList(t *testing.T) {
	cores, err := ParseCPUList(" 2-4, 8 ,3")
	require.NoError(t, err)
	assert.Equal(t, []int{2, 3, 4, 8}, cores, "sorted and without duplicates")

	cores, err = ParseCPUList("")
	require.NoError(t, err)
	assert.Nil(t, cores, "empty means no pinning")

	for _, bad := range []string{"a", "3-1", "1-", "-2", "1,,2", "0-99999"} {
		_, err := ParseCPUList(bad)
		assert.Error(t, err, bad)
	}
}

// pinnedIsolate stands in for isolate: --run of the probe writes the
// Cpus_allowed_list that the box would really have.
type pinnedIsolate struct {
	root string
	// cores is the core each box id really runs on, as isolate's boxN.cpus lines would set it.
	cores map[int]string
	mu    sync.Mutex
	calls []string
}

func (f *pinnedIsolate) run(_ context.Context, _ string, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	var boxID int
	for _, arg := range args {
		if value, found := strings.CutPrefix(arg, "--box-id="); found {
			boxID, _ = strconv.Atoi(value)
		}
	}
	switch args[len(args)-1] {
	case "--init":
		dir := filepath.Join(f.root, strconv.Itoa(boxID))
		_ = os.MkdirAll(filepath.Join(dir, "box"), 0o755)
		return []byte(dir + "\n"), nil, 0, nil
	case "--cleanup", "--version":
		return nil, nil, 0, nil
	}
	for _, arg := range args {
		if path, found := strings.CutPrefix(arg, "--meta="); found {
			_ = os.WriteFile(path, nil, 0o600)
		}
		if name, found := strings.CutPrefix(arg, "--stdout="); found {
			status := fmt.Sprintf("Name:\tcat\nCpus_allowed:\tff\nCpus_allowed_list:\t%s\nMems_allowed:\t1\n", f.cores[boxID])
			_ = os.WriteFile(filepath.Join(f.root, strconv.Itoa(boxID), "box", name), []byte(status), 0o644)
		}
	}
	return nil, nil, 0, nil
}

func newPinnedSandbox(t *testing.T, fake *pinnedIsolate, cpus []int) *Isolate {
	t.Helper()
	cgroupRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpuset cpu memory pids\n"), 0o600))
	sandbox, err := NewIsolate(IsolateConfig{
		IsolatePath: "isolate", CgroupRoot: cgroupRoot, BoxIDBase: 100, BoxCount: 2, MetaDir: t.TempDir(),
		Runner: fake.run, CPUs: cpus,
	})
	require.NoError(t, err)
	return sandbox
}

func TestCoreForAssignsOneCorePerBox(t *testing.T) {
	sandbox := newPinnedSandbox(t, &pinnedIsolate{root: t.TempDir()}, []int{4, 5})
	assert.Equal(t, 4, sandbox.CoreFor(100))
	assert.Equal(t, 5, sandbox.CoreFor(101))
	assert.Equal(t, -1, newPinnedSandbox(t, &pinnedIsolate{root: t.TempDir()}, nil).CoreFor(100), "no list, no pinning")
}

func TestStartupProbeAcceptsBoxesOnTheirCores(t *testing.T) {
	fake := &pinnedIsolate{root: t.TempDir(), cores: map[int]string{100: "4", 101: "5"}}
	sandbox := newPinnedSandbox(t, fake, []int{4, 5})
	require.NoError(t, sandbox.CheckEnvironment(context.Background()))

	// Both boxes were probed and handed back.
	cleanups := 0
	for _, call := range fake.calls {
		if strings.HasSuffix(call, "--cleanup") {
			cleanups++
		}
	}
	assert.Equal(t, 3, cleanups, "the first probe box and the two pinning probe boxes")
	box, err := sandbox.Acquire(context.Background())
	require.NoError(t, err, "every box id is back in the pool")
	require.NoError(t, box.Cleanup())
}

func TestStartupProbeRefusesABoxThatIsNotPinned(t *testing.T) {
	// isolate has no "box101.cpus" line, so the box may run anywhere.
	fake := &pinnedIsolate{root: t.TempDir(), cores: map[int]string{100: "4", 101: "0-9"}}
	err := newPinnedSandbox(t, fake, []int{4, 5}).CheckEnvironment(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "box 101")
	assert.Contains(t, err.Error(), `box101.cpus = 5`)
}

func TestNothingIsProbedWithoutCPUs(t *testing.T) {
	fake := &pinnedIsolate{root: t.TempDir()}
	require.NoError(t, newPinnedSandbox(t, fake, nil).CheckEnvironment(context.Background()))
	for _, call := range fake.calls {
		assert.NotContains(t, call, "--run")
	}
}

func TestCheckEnvironmentNeedsCpusetWhenPinning(t *testing.T) {
	cgroupRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600))
	sandbox, err := NewIsolate(IsolateConfig{
		IsolatePath: "isolate", CgroupRoot: cgroupRoot, BoxIDBase: 100, BoxCount: 1, MetaDir: t.TempDir(),
		Runner: (&pinnedIsolate{root: t.TempDir()}).run, CPUs: []int{1},
	})
	require.NoError(t, err)
	assert.ErrorContains(t, sandbox.CheckEnvironment(context.Background()), `controller "cpuset"`)
}
