// Package sandbox runs untrusted programs inside isolate boxes.
//
// Every box is created fresh (init), used for a single run, and always
// cleaned up, even when the run fails or the context is cancelled: control
// group accounting such as the OOM flag survives between runs in the same
// box, so reusing a box would leak state from one test into the next.
package sandbox

import (
	"context"
	"time"
)

// Status is the outcome of one sandboxed run, before it is mapped to a
// verdict.
type Status string

const (
	StatusOK           Status = "OK"            // exited with code 0
	StatusRuntimeError Status = "RUNTIME_ERROR" // exited with a non-zero code
	StatusSignaled     Status = "SIGNALED"      // killed by a signal (other than below)
	StatusTimeLimit    Status = "TIME_LIMIT"    // CPU or wall time limit
	StatusMemoryLimit  Status = "MEMORY_LIMIT"  // killed by the cgroup OOM killer
	StatusOutputLimit  Status = "OUTPUT_LIMIT"  // a file grew past the size limit (SIGXFSZ)
	StatusInternal     Status = "INTERNAL"      // the sandbox itself failed
)

// Limits for a single run. Zero means "use isolate's default" except where
// noted; callers always set CPUTime, WallTime and MemoryKB.
type Limits struct {
	CPUTime   time.Duration
	WallTime  time.Duration
	ExtraTime time.Duration
	// MemoryKB limits the whole control group (all processes together).
	MemoryKB int
	// Processes limits processes and threads; 1 means a single thread.
	Processes int
	// FileSizeKB limits the size of every file the program writes,
	// including redirected stdout.
	FileSizeKB int
	StackKB    int
	OpenFiles  int
}

// RunRequest describes one run. File names are relative to the box
// directory, which is /box inside the sandbox and Box.Dir() outside.
type RunRequest struct {
	Args   []string
	Stdin  string
	Stdout string
	Stderr string
	// StderrToStdout sends stderr into the stdout file (compiler messages).
	StderrToStdout bool
	// Env is the complete environment, "KEY=value"; nothing is inherited.
	Env    []string
	Limits Limits
}

// RunResult is what isolate reported in its meta file.
type RunResult struct {
	Status    Status
	ExitCode  int
	Signal    int
	CPUTime   time.Duration
	WallTime  time.Duration
	MaxRSSKB  int
	CgroupKB  int
	OOMKilled bool
	Message   string
}

// Box is one initialised sandbox.
type Box interface {
	// Dir is the host path of the box's working directory (/box inside).
	Dir() string
	Run(ctx context.Context, request RunRequest) (RunResult, error)
	// Cleanup removes the box and returns its id to the pool. Safe to call
	// more than once.
	Cleanup() error
}

// Sandbox hands out fresh boxes.
type Sandbox interface {
	// Acquire waits for a free box id, initialises the box and returns it.
	Acquire(ctx context.Context) (Box, error)
	// CheckEnvironment verifies that the host can run sandboxes (cgroup v2,
	// isolate installed and able to create a box). Judge refuses to start
	// when it fails.
	CheckEnvironment(ctx context.Context) error
}
