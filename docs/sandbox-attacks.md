# The sandbox attack suite (GRD-57)

`make test-integration` judges hostile programs from `examples/attacks/` in the
real sandbox (isolate, cgroup v2, a privileged container on Linux) and checks
three things after each one:

1. **The verdict is a safe one** (never AC, never ISE) and Judge answered within the time allowed.
2. **Nothing of the host reached the result.** A canary file with a random secret lives on the host; programs try to read it, overwrite it, or make Judge read it for them. The secret must not appear anywhere in the result, and the file must be unchanged.
3. **Nothing is left behind**: no process of a sandbox user, no box directory, no control group.

CI runs the suite on every pull request (`.github/workflows/ci.yml`, job `integration`, an Ubuntu 24.04 runner with cgroup v2).

| Attack | File | Held by | Verdict |
| --- | --- | --- | --- |
| Fork bomb | `forkbomb.c` | one process allowed; memory and time limits | TLE, RE or MLE |
| Thread bomb | `threadbomb.py` | threads count as processes | WA, 0 threads started |
| Memory bomb | `membomb.c` | memory limit of the whole control group | MLE |
| Unbounded recursion | `stackbomb.c` | stack limit, memory limit | RE or MLE |
| Endless loop | `loop.c` | CPU limit | TLE |
| Sleeping forever | `sleeper.py` | wall-clock limit | TLE |
| Endless output | `bigoutput.c` | file size limit | OLE |
| Fill the disk with many files | `diskfill.c` | files in the box count as memory of the control group; time limit; box removed afterwards | MLE, TLE or OLE |
| Open files without end | `openfiles.py` | `open_files = 64` | WA, at most 64 handles |
| Replace its output with a link to a host file | `symlink_output.py`, `symlink_stderr.py` | isolate removes special files after a run, **and** Judge never follows a link or opens a pipe (`openSandboxFile`) | WA, nothing returned |
| Replace its output with a named pipe | `fifo_output.py` | the same; opening it for reading would block the worker until a writer appeared | WA |
| Reach the network (TCP to the internet, localhost and the host gateway, UDP, Docker socket, raw sockets) | `probe_host.py` | own empty network namespace | every line "blocked" |
| Read host files (`/etc/shadow`, `/root`, `/proc/kcore`, `/dev/mem`, the cgroup tree, the canary) | `probe_host.py` | only read-only system directories and `/box` are mounted; unprivileged uid | "blocked" |
| Write outside the box (`/usr`, `/etc`, `/dev`, `/proc/sys`, `/proc/sysrq-trigger`, the canary) | `probe_host.py` | read-only mounts; unprivileged uid | "blocked" |
| Gain privileges (`setuid(0)`, `chroot`, `mount`, `mknod`, `reboot`, `sethostname`, new user namespace) | `probe_host.py` | unprivileged uid, no capabilities | "blocked" |
| `ptrace` another process, see other processes | `probe_host.py` | own pid namespace: only itself is visible | "blocked", 1 process |

`escape.py` (the older, smaller probe) stays in `examples/aplusb/solutions/`.

## What was found

- **No escape.** Every attack ended in a safe verdict on the first full run (Docker on Linux 6.12, isolate 2.7).
- **Defence in depth added.** Judge reads files that a program could have replaced (output, error output, the compiled program). Until now it relied on isolate deleting special files after a run; it now opens them with `O_NOFOLLOW`, refuses anything that is not a regular file, and treats a refused file as empty (`services/pipeline/sandboxfile.go`, unit test `sandboxfile_test.go`). Without it, a link would be followed with Judge's own privileges, and a named pipe could stop a worker.
- **Disk exhaustion is bounded by memory, not by a disk quota.** Written pages count against the box's memory limit, so `diskfill.c` ends as MLE (or TLE) long before a disk is full. We have no per-box disk quota; the plan if that ever stops being true (a different filesystem, `fsync` tricks) is to put `box_root` on its own size-limited filesystem. The runbook lists `df` on the box root among the things to watch.
- **No syscall filter beyond isolate's own.** isolate restricts a few system calls by default (`syscall_flags`); there is no seccomp profile of ours. A kernel vulnerability reachable from an unprivileged process in a fresh namespace would still be an escape. That is the reason for the hardening of the machine itself (GRD-41): a user without privileges, no database credentials, no inbound access except from Core, and the ability to wipe and rebuild the machine from the runbook.

## Before launch (not done yet)

The suite must also pass **on the real Judge machine**, not only in CI. With Judge stopped:

```bash
sudo INTEGRATION_BOX_BASE=100 INTEGRATION_CPUS=<sandbox.cpuset> go test -count=1 -tags integration -v ./integration/...
```

(the tests need the Go toolchain and `/usr/local/bin/isolate`; build a test binary with `go test -c -tags integration ./integration` elsewhere and copy it if Go is not on the machine.) Record the date and the commit in the runbook's launch checklist.

## Adding an attack

Put the program in `examples/attacks/`, add a row to the table in `integration/attack_test.go` with the verdicts that count as safe, and, when it is meant to leak something, make it print or return the canary (`__CANARY__` in the source is replaced by the host path).
