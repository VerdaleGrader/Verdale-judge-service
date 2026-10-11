package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMeta(t *testing.T) {
	cases := map[string]struct {
		meta   string
		status Status
	}{
		"normal exit":      {"time:0.045\ntime-wall:0.046\nmax-rss:27880\ncg-mem:7200\nexitcode:0\n", StatusOK},
		"non-zero exit":    {"status:RE\nmessage:Exited with error status 1\nexitcode:1\ntime:0.010\n", StatusRuntimeError},
		"time limit":       {"status:TO\nmessage:Time limit exceeded\nkilled:1\ntime:1.005\ntime-wall:1.005\n", StatusTimeLimit},
		"oom kill":         {"cg-mem:65536\ncg-oom-killed:1\nexitsig:9\nstatus:SG\nmessage:Caught fatal signal 9\n", StatusMemoryLimit},
		"segfault":         {"exitsig:11\nstatus:SG\nmessage:Caught fatal signal 11\n", StatusSignaled},
		"file size limit":  {"exitsig:25\nstatus:SG\nmessage:Caught fatal signal 25\n", StatusOutputLimit},
		"sandbox failure":  {"status:XX\nmessage:Cannot run proxy\n", StatusInternal},
		"oom wins over RE": {"cg-oom-killed:1\nstatus:RE\nexitcode:1\n", StatusMemoryLimit},
		"empty meta":       {"", StatusOK},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, testCase.status, ParseMeta([]byte(testCase.meta)).Status)
		})
	}

	result := ParseMeta([]byte("time:1.250\ntime-wall:2.5\nmax-rss:1180\ncg-mem:7200\nexitcode:3\nstatus:RE\n"))
	assert.Equal(t, 1250*time.Millisecond, result.CPUTime)
	assert.Equal(t, 2500*time.Millisecond, result.WallTime)
	assert.Equal(t, 1180, result.MaxRSSKB)
	assert.Equal(t, 7200, result.CgroupKB)
	assert.Equal(t, 3, result.ExitCode)
}

func TestBuildRunArgs(t *testing.T) {
	args := BuildRunArgs(7, "/meta/x", RunRequest{
		Args:   []string{"./main"},
		Stdin:  "input.txt",
		Stdout: "output.txt",
		Env:    []string{"PATH=/usr/bin:/bin"},
		Limits: Limits{
			CPUTime: 1500 * time.Millisecond, WallTime: 5 * time.Second, ExtraTime: 500 * time.Millisecond,
			MemoryKB: 262144, Processes: 1, FileSizeKB: 65536, StackKB: 262144, OpenFiles: 64,
		},
	})
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--cg", "--box-id=7", "--meta=/meta/x", "--time=1.500", "--wall-time=5.000", "--extra-time=0.500",
		"--cg-mem=262144", "--processes=1", "--fsize=65536", "--stack=262144", "--open-files=64",
		"--stdin=input.txt", "--stdout=output.txt", "--env=PATH=/usr/bin:/bin", "--core=0",
	} {
		assert.Contains(t, args, expected)
	}
	assert.True(t, strings.HasSuffix(joined, "--run -- ./main"))
	assert.NotContains(t, joined, "--share-net", "the network must stay off")
	assert.NotContains(t, joined, "--dir", "no host directories beyond isolate's defaults")
}

type fakeIsolate struct {
	mu        sync.Mutex
	calls     []string
	initFails bool
	runExit   int
	meta      string
}

func (f *fakeIsolate) run(_ context.Context, _ string, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	last := args[len(args)-1]
	switch {
	case last == "--init":
		if f.initFails {
			return nil, []byte("Box is busy"), 2, nil
		}
		return []byte("/var/local/lib/isolate/100\n"), nil, 0, nil
	case last == "--cleanup", last == "--version":
		return nil, nil, 0, nil
	}
	for _, arg := range args {
		if path, found := strings.CutPrefix(arg, "--meta="); found {
			_ = os.WriteFile(path, []byte(f.meta), 0o600)
		}
	}
	return nil, nil, f.runExit, nil
}

func newFakeSandbox(t *testing.T, fake *fakeIsolate, cgroupRoot string) *Isolate {
	t.Helper()
	sandbox, err := NewIsolate(IsolateConfig{
		IsolatePath: "isolate", CgroupRoot: cgroupRoot, BoxIDBase: 100, BoxCount: 1,
		MetaDir: t.TempDir(), Runner: fake.run,
	})
	require.NoError(t, err)
	return sandbox
}

func TestBoxLifecycle(t *testing.T) {
	fake := &fakeIsolate{runExit: 1, meta: "status:TO\ntime:2.001\n"}
	sandbox := newFakeSandbox(t, fake, t.TempDir())
	ctx := context.Background()

	box, err := sandbox.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, "/var/local/lib/isolate/100/box", box.Dir())

	result, err := box.Run(ctx, RunRequest{Args: []string{"./main"}, Limits: Limits{CPUTime: time.Second, WallTime: 3 * time.Second, MemoryKB: 65536}})
	require.NoError(t, err)
	assert.Equal(t, StatusTimeLimit, result.Status)

	require.NoError(t, box.Cleanup())
	require.NoError(t, box.Cleanup(), "cleanup is idempotent")

	// The single box id went back to the pool exactly once.
	second, err := sandbox.Acquire(ctx)
	require.NoError(t, err)
	require.NoError(t, second.Cleanup())
	cleanups := 0
	for _, call := range fake.calls {
		if strings.HasSuffix(call, "--cleanup") {
			cleanups++
		}
	}
	assert.Equal(t, 2, cleanups)
}

func TestRunReportsSandboxFailure(t *testing.T) {
	fake := &fakeIsolate{runExit: 2}
	sandbox := newFakeSandbox(t, fake, t.TempDir())
	box, err := sandbox.Acquire(context.Background())
	require.NoError(t, err)
	defer box.Cleanup()

	result, err := box.Run(context.Background(), RunRequest{Args: []string{"./main"}})
	assert.Error(t, err)
	assert.Equal(t, StatusInternal, result.Status)
}

func TestAcquireReturnsIDWhenInitFails(t *testing.T) {
	fake := &fakeIsolate{initFails: true}
	sandbox := newFakeSandbox(t, fake, t.TempDir())
	_, err := sandbox.Acquire(context.Background())
	require.Error(t, err)

	fake.initFails = false
	box, err := sandbox.Acquire(context.Background())
	require.NoError(t, err, "the id must be back in the pool")
	require.NoError(t, box.Cleanup())
}

func TestAcquireHonoursContext(t *testing.T) {
	sandbox := newFakeSandbox(t, &fakeIsolate{}, t.TempDir())
	box, err := sandbox.Acquire(context.Background())
	require.NoError(t, err)
	defer box.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = sandbox.Acquire(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "no free box until the first one is cleaned up")
}

func TestCheckEnvironmentRequiresCgroupV2(t *testing.T) {
	sandbox := newFakeSandbox(t, &fakeIsolate{}, t.TempDir())
	err := sandbox.CheckEnvironment(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cgroup v2 is not mounted")

	cgroupRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpuset cpu io\n"), 0o600))
	err = newFakeSandbox(t, &fakeIsolate{}, cgroupRoot).CheckEnvironment(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `controller "memory"`)

	require.NoError(t, os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpuset cpu io memory pids\n"), 0o600))
	assert.NoError(t, newFakeSandbox(t, &fakeIsolate{}, cgroupRoot).CheckEnvironment(context.Background()))
}
