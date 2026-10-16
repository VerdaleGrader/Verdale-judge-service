# Verdale Judge service

Runs untrusted code (C, C++, Python) in an [isolate](https://github.com/ioi/isolate) sandbox and reports a verdict. Core sends jobs over an HMAC-signed internal API; Judge queues them in Redis Streams, judges them, and POSTs a signed result back to Core. Judge never touches the database.

```
Core --POST /internal/jobs (signed)--> Judge API --> Redis Stream --> workers --> isolate boxes
  ^                                                                      |
  +----------- signed result callback <-- outbox (Redis) <---------------+
```

## Quick start (local)

Needs Docker (the sandbox needs Linux with cgroup v2, so Judge runs in a privileged container locally) and Go 1.24.

```bash
cp .env.example .env
make up                                   # Redis + Judge + mock Core
make submit SOURCE=examples/aplusb/solutions/ac.cpp
make logs                                 # the mock Core prints each result
```

`make submit-all` sends every example solution; each is named after its expected verdict (see `examples/aplusb/README.md`). Results are also saved to `tmp/results/<job_id>.json`.

## Commands

| Command | What |
| --- | --- |
| `make check` | Everything CI runs: gofmt, `go vet`, unit tests with `-race`, contract check |
| `make test-integration` | Real isolate + real Redis in a privileged container: every verdict, the attack suite ([docs/sandbox-attacks.md](docs/sandbox-attacks.md)), one core per box, worker kill/reclaim |
| `make benchmark` / `make benchmark-host` | How steady is the measured time? Saves a report in `docs/benchmarks/` ([docs/timing.md](docs/timing.md)) |
| `make test-resilience` | Against `make up`: SIGKILL Judge mid-job, Redis restart with queued jobs, Core down |
| `make contracts-sync` | Re-pin contracts from `../Verdale-core-service/contracts` and regenerate types |
| `make build` / `make run` | Build or run the binary on a Linux host |

## Layout

Follows the same layering as the other Go services: controllers → services → repositories, with shared packages under `pkg/`.

```
main.go                  wiring, startup checks, graceful shutdown
configs/                 config.json + VERDALE_JUDGE_* env overrides, validated at start
routes/                  HTTP routes (/health, /ready, /metrics, /internal/*)
controllers/             HTTP handlers (jobs, system)
services/jobs            validation, idempotency, callback allow-list
services/worker          queue consumer: heartbeat, retry, reclaim, dead-letter
services/pipeline        compile -> run each test in a fresh box -> compare -> verdict
services/callback        signed result delivery with backoff from the outbox
repositories/            Redis-backed job state (jobstore) and result outbox
pkg/sandbox              isolate driver: box lifecycle, limits, meta parsing, cgroup v2 check
pkg/runner               language table (c, cpp, python), toolchain version pin
pkg/testfiles            test files by sha256 from R2 (read-only), verified, LRU disk cache
pkg/comparator           streaming output checkers (line checker; extensible)
pkg/queue                Redis Streams consumer group
pkg/hmacauth             request signing (contracts/HMAC.md)
pkg/contract             types generated from contracts/judge.openapi.yaml (do not edit)
cmd/submit, cmd/mockcore Milestone 1 tools: send signed jobs, receive results
contracts/               pinned copy of Verdale-core-service/contracts (see contracts.lock.json)
deploy/                  container entrypoint and isolate config; install.sh, systemd unit, firewall for production (docs/runbook.md)
examples/attacks         hostile programs for the sandbox attack suite; examples/benchmarks: programs for the timing benchmark
```

## Behaviour worth knowing

- **Verdicts**: AC, WA, TLE, MLE, RE, CE, OLE, ISE. A Python syntax error is CE (checked with `py_compile` in the sandbox). Output that reaches the output limit is OLE even if the program then crashes. ISE is reported only after the job failed on every delivery, and it is never the submitter's fault.
- **Submission vs verify**: `submission` stops at the first failing test (the rest are SKIPPED); `verify` runs every test.
- **Hidden tests never leak**: only tests marked `sample` carry an output preview or checker message in the result, unless Core asks for `capture` (below).
- **Capture** (contract 0.4.0): a job may carry `capture: {stdout_bytes, stderr_bytes, total_bytes}`. Every test that ran then returns the start of its standard output and error (`stdout`, `stderr`, and their full sizes), hidden tests included, because Core decides who may see them. Judge keeps to `total_bytes` for the whole job (default 2 MiB); once it is used up, later tests come back without text. Text is valid UTF-8 and standard error has host paths removed.
- **Generate** (contract 0.4.0): `kind: generate` runs every test input through the program and returns the complete output of each test, without comparing; `tests[].output` may be left out and is not downloaded. It needs `capture.stdout_bytes`; a test whose output is larger than that is reported as `OLE` rather than returning a cut answer. Core uses it to create expected outputs from a reference solution.
- **Sandbox**: a fresh box per compile and per test, always cleaned up. No network (own network namespace), only read-only system dirs plus `/box`, one process, CPU/wall/memory/output/stack limits. Every box is locked to one core (`sandbox.cpuset`, set up by isolate's `boxN.cpus`), so parallel jobs never share a core; Judge refuses to start without cgroup v2, a working isolate, boxes on their cores, or the pinned compiler versions. Files a program could have replaced (its output, its compiled binary) are never followed as links and never opened if they are not regular files.
- **Concurrency**: `worker.concurrency` jobs at once, one box each; `0` means one per core of `sandbox.cpuset`. On SIGTERM Judge stops taking jobs and lets the running ones finish and store their results.
- **Delivery**: at least once. A job is acked only after its result is in the outbox; a worker that dies stops heartbeating and its job is reclaimed after `queue.reclaim_idle_seconds`; after `queue.max_deliveries` it goes to `judge:jobs:dead` and Core gets ISE. Results are retried with exponential backoff for `callback.retry_window_minutes`, then moved to the dead outbox.
- **Shutdown**: SIGTERM stops taking jobs, waits up to `worker.shutdown_timeout_seconds` for running ones, then exits; anything unfinished is reclaimed later.

## Configuration

`config.json` holds defaults and no secrets. Every key can be overridden with `VERDALE_JUDGE_` + the upper-cased path, e.g. `server.port` → `VERDALE_JUDGE_SERVER_PORT`. Secrets are env-only; `.env.example` documents each one. Startup fails with the full list of problems when anything is missing.

## Metrics

`GET /metrics` (Prometheus): `judge_queue_depth`, `judge_queue_oldest_job_age_seconds`, `judge_queue_dead_letter_depth`, `judge_outbox_pending`, `judge_outbox_dead`, `judge_callback_failures_total`, `judge_jobs_completed_total`, `judge_job_duration_seconds`, `judge_system_errors_total`.

## Production

[docs/runbook.md](docs/runbook.md): building a machine (`deploy/install.sh`), updating the runtime, what to do when something is wrong. [deploy/systemd/README.md](deploy/systemd/README.md) has the short version, [docs/timing.md](docs/timing.md) the time-limit policy.
