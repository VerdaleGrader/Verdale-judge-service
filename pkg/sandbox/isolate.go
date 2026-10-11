package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// signalFileSizeExceeded is SIGXFSZ on Linux: the program wrote past the
// file size limit, which is how an oversized stdout shows up.
const signalFileSizeExceeded = 25

// CommandRunner runs an external command and returns its stdout, stderr
// and exit code. err is set only when the command could not be run at all.
// It is a seam for unit tests.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)

// ExecRunner runs commands with os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

type IsolateConfig struct {
	IsolatePath string
	CgroupRoot  string
	BoxIDBase   int
	BoxCount    int
	// MetaDir is a host directory, outside every box, for isolate's meta
	// files so the sandboxed program cannot tamper with them.
	MetaDir string
	Runner  CommandRunner
}

// Isolate is the Sandbox backed by the isolate binary in control group mode.
type Isolate struct {
	config  IsolateConfig
	freeIDs chan int
	runs    atomic.Int64
}

func NewIsolate(config IsolateConfig) (*Isolate, error) {
	if config.BoxCount < 1 {
		return nil, errors.New("isolate: box count must be positive")
	}
	if config.Runner == nil {
		config.Runner = ExecRunner
	}
	if err := os.MkdirAll(config.MetaDir, 0o700); err != nil {
		return nil, fmt.Errorf("isolate: create meta dir: %w", err)
	}
	freeIDs := make(chan int, config.BoxCount)
	for offset := 0; offset < config.BoxCount; offset++ {
		freeIDs <- config.BoxIDBase + offset
	}
	return &Isolate{config: config, freeIDs: freeIDs}, nil
}

func (s *Isolate) CheckEnvironment(ctx context.Context) error {
	controllers, err := os.ReadFile(filepath.Join(s.config.CgroupRoot, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("cgroup v2 is not mounted at %s (isolate needs the unified hierarchy): %w", s.config.CgroupRoot, err)
	}
	available := strings.Fields(string(controllers))
	for _, required := range []string{"memory", "pids"} {
		if !contains(available, required) {
			return fmt.Errorf("cgroup v2 controller %q is not available at %s (have: %s)", required, s.config.CgroupRoot, strings.Join(available, " "))
		}
	}
	if _, stderr, exitCode, err := s.config.Runner(ctx, s.config.IsolatePath, "--version"); err != nil || exitCode != 0 {
		return fmt.Errorf("isolate is not runnable at %s: %v %s", s.config.IsolatePath, err, strings.TrimSpace(string(stderr)))
	}
	// A real init/cleanup proves permissions, the config file and the
	// delegated control group are all in order.
	box, err := s.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("isolate could not create a probe box: %w", err)
	}
	return box.Cleanup()
}

func (s *Isolate) Acquire(ctx context.Context) (Box, error) {
	var boxID int
	select {
	case boxID = <-s.freeIDs:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	stdout, stderr, exitCode, err := s.config.Runner(ctx, s.config.IsolatePath, "--cg", "--box-id="+strconv.Itoa(boxID), "--init")
	if err != nil || exitCode != 0 {
		s.freeIDs <- boxID
		return nil, fmt.Errorf("isolate --init box %d failed (exit %d): %v %s", boxID, exitCode, err, strings.TrimSpace(string(stderr)))
	}
	root := strings.TrimSpace(string(stdout))
	if root == "" {
		s.freeIDs <- boxID
		return nil, fmt.Errorf("isolate --init box %d printed no path", boxID)
	}
	return &isolateBox{sandbox: s, id: boxID, dir: filepath.Join(root, "box")}, nil
}

type isolateBox struct {
	sandbox     *Isolate
	id          int
	dir         string
	cleanupOnce sync.Once
	cleanupErr  error
}

func (b *isolateBox) Dir() string { return b.dir }

func (b *isolateBox) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if len(request.Args) == 0 {
		return RunResult{}, errors.New("isolate: no program to run")
	}
	metaPath := filepath.Join(b.sandbox.config.MetaDir, fmt.Sprintf("box-%d-run-%d.meta", b.id, b.sandbox.runs.Add(1)))
	defer os.Remove(metaPath)

	args := BuildRunArgs(b.id, metaPath, request)
	_, stderr, exitCode, err := b.sandbox.config.Runner(ctx, b.sandbox.config.IsolatePath, args...)
	if err != nil {
		return RunResult{}, fmt.Errorf("isolate --run box %d could not start: %w", b.id, err)
	}
	// 0: the program succeeded, 1: the program failed (details in meta),
	// anything else: isolate itself failed.
	if exitCode != 0 && exitCode != 1 {
		return RunResult{Status: StatusInternal}, fmt.Errorf("isolate --run box %d failed (exit %d): %s", b.id, exitCode, strings.TrimSpace(string(stderr)))
	}
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		return RunResult{Status: StatusInternal}, fmt.Errorf("isolate box %d: read meta file: %w", b.id, err)
	}
	result := ParseMeta(meta)
	if result.Status == StatusInternal {
		return result, fmt.Errorf("isolate box %d reported an internal error: %s", b.id, result.Message)
	}
	return result, nil
}

func (b *isolateBox) Cleanup() error {
	b.cleanupOnce.Do(func() {
		// Cleanup must happen even when the job's context was cancelled.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, stderr, exitCode, err := b.sandbox.config.Runner(ctx, b.sandbox.config.IsolatePath, "--cg", "--box-id="+strconv.Itoa(b.id), "--cleanup")
		if err != nil || exitCode != 0 {
			b.cleanupErr = fmt.Errorf("isolate --cleanup box %d failed (exit %d): %v %s", b.id, exitCode, err, strings.TrimSpace(string(stderr)))
		}
		// The id goes back either way: the next --init resets the box.
		b.sandbox.freeIDs <- b.id
	})
	return b.cleanupErr
}

// BuildRunArgs builds the isolate command line for one run. The network is
// off because isolate gives every run its own empty network namespace (we
// never pass --share-net), and only isolate's default read-only system
// directories plus /box are visible.
func BuildRunArgs(boxID int, metaPath string, request RunRequest) []string {
	limits := request.Limits
	args := []string{
		"--cg",
		"--box-id=" + strconv.Itoa(boxID),
		"--meta=" + metaPath,
		"--silent",
		"--time=" + seconds(limits.CPUTime),
		"--wall-time=" + seconds(limits.WallTime),
		"--extra-time=" + seconds(limits.ExtraTime),
		"--cg-mem=" + strconv.Itoa(limits.MemoryKB),
		"--processes=" + strconv.Itoa(max(limits.Processes, 1)),
		"--core=0",
	}
	if limits.FileSizeKB > 0 {
		args = append(args, "--fsize="+strconv.Itoa(limits.FileSizeKB))
	}
	if limits.StackKB > 0 {
		args = append(args, "--stack="+strconv.Itoa(limits.StackKB))
	}
	if limits.OpenFiles > 0 {
		args = append(args, "--open-files="+strconv.Itoa(limits.OpenFiles))
	}
	if request.Stdin != "" {
		args = append(args, "--stdin="+request.Stdin)
	}
	if request.Stdout != "" {
		args = append(args, "--stdout="+request.Stdout)
	}
	if request.StderrToStdout {
		args = append(args, "--stderr-to-stdout")
	} else if request.Stderr != "" {
		args = append(args, "--stderr="+request.Stderr)
	}
	for _, variable := range request.Env {
		args = append(args, "--env="+variable)
	}
	args = append(args, "--run", "--")
	return append(args, request.Args...)
}

// ParseMeta turns an isolate meta file into a RunResult.
func ParseMeta(meta []byte) RunResult {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(meta))
	for scanner.Scan() {
		if key, value, found := strings.Cut(scanner.Text(), ":"); found {
			values[key] = value
		}
	}
	result := RunResult{
		ExitCode:  atoi(values["exitcode"]),
		Signal:    atoi(values["exitsig"]),
		CPUTime:   parseSeconds(values["time"]),
		WallTime:  parseSeconds(values["time-wall"]),
		MaxRSSKB:  atoi(values["max-rss"]),
		CgroupKB:  atoi(values["cg-mem"]),
		Message:   values["message"],
		OOMKilled: values["cg-oom-killed"] != "",
	}
	switch {
	case values["status"] == "XX":
		result.Status = StatusInternal
	case result.OOMKilled:
		result.Status = StatusMemoryLimit
	case values["status"] == "TO":
		result.Status = StatusTimeLimit
	case values["status"] == "SG" && result.Signal == signalFileSizeExceeded:
		result.Status = StatusOutputLimit
	case values["status"] == "SG":
		result.Status = StatusSignaled
	case values["status"] == "RE":
		result.Status = StatusRuntimeError
	default:
		result.Status = StatusOK
	}
	return result
}

func seconds(duration time.Duration) string {
	return strconv.FormatFloat(duration.Seconds(), 'f', 3, 64)
}

func parseSeconds(raw string) time.Duration {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return time.Duration(value * float64(time.Second))
}

func atoi(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
