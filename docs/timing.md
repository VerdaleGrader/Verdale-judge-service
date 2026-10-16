# Time measurement: policy, precision, and how to check it (GRD-42, GRD-40)

An author sets a time limit and a solver gets TLE when the program is slower.
That is only fair if the same program is measured at about the same time
every time. This page says what Judge measures, what it promises, and how to
check the promise on a real machine.

## What is measured

| Item | Rule |
| --- | --- |
| Quantity | CPU time of the program (user and system time of its control group), not wall time. Waiting for input or sleeping costs nothing. |
| Resolution | isolate reports milliseconds with three decimals; Judge reports `time_ms` rounded **down** to a whole millisecond. |
| TLE | isolate stops the program when its CPU time passes `limits.time_ms`. It is allowed `sandbox.extra_time_ms` (500 ms, never more than the limit itself) of grace so the real time is reported. |
| Sleepers | A wall-clock limit of `3 × time limit + 1 s` stops a program that sleeps or blocks. |
| Compile time | Not counted against the author's limit; it has its own limits (`sandbox.compile`). |
| One core each | Every box is locked to one core of `sandbox.cpuset` by isolate (`boxN.cpus`), so parallel jobs never share a core and a program cannot move itself (`sched_setaffinity` is refused). Judge's startup probe checks every box and refuses to start if one is not locked. |
| Concurrency | `worker.concurrency` is at most the number of cores in `sandbox.cpuset` (`0` = exactly that number). One thread per physical core only: `install.sh` leaves out the second hyper-thread of every core. |

## Policy for time limits

1. **Limit = 2 × the slowest reference time, rounded up to the next 100 ms** (never below 100 ms, the least Core accepts). The verify run at publish time measures the reference solutions on the production Judge, so the author has the number.
2. If Python (or another slow language) is allowed, base the limit on a reference solution **in that language**, or add the difference by hand: the same limit applies to every language.
3. Never set a limit below **5 ms of expected spread**: a millisecond is the resolution, so tests that finish in a few milliseconds are measured with a relative error of tens of percent. Short tests do not discriminate between solutions; make them bigger or accept that they only check correctness.
4. A solution's time on one machine says little about another. When the Judge machine, its kernel, the compilers or isolate change, run the benchmark again and compare (below); if a median moves by more than 10 %, look at the limits of problems that sit near them.

Why a factor of 2: on the laptop VM measured below the spread of a compiled CPU-bound program is under 10 %, running four jobs together adds a further 10 to 35 % for CPU-bound and interpreted programs, and more than 100 % for programs that saturate memory bandwidth. A dedicated machine should do better; the benchmark on it decides whether 2 is too much or too little. Pinning stops the scheduler from making it worse, but cannot stop jobs from sharing the memory bus and last-level cache.

## Precision to expect

Measured with `make benchmark` (docs/benchmarks/20261016-164834-docker-desktop.json), 8 rounds, 4 jobs together, on a **laptop VM** (Docker Desktop on a Mac, 10 vCPUs, kernel 6.12 linuxkit). That is a worst case: shared cores, no control of frequency or neighbours.

| Program | Alone: median ms | Alone: p95 above median | Together (4 jobs): median | Slowdown with company |
| --- | ---: | ---: | ---: | ---: |
| `cpu.c` (dependent integer operations) | 616 | 5.7 % | 697 | 13 % |
| `sort.cpp` (sorting 2 M numbers) | 99 | 7.1 % | 108 | 9 % |
| `memory.c` (strided walk over 96 MB) | 413 | 12.6 % | 907 | **120 %** |
| `output.c` (30 MB of numbers) | 91 | 41.8 % | 120 | 32 % |
| `interpreter.py` (Python loop) | 208 | 27.4 % | 284 | 37 % |

What to take from it:

- Alone, compiled CPU-bound code is steady to within about 6 % even on this VM; the spread of I/O and interpreted programs is larger.
- Two jobs on different cores still slow each other when both are memory-bound. This is a property of the hardware, not of Judge. The remedy is fewer concurrent jobs per socket, not more pinning.
- **These figures are not the figures of the Judge machine.** The benchmark gate (`BENCHMARK_MAX_SPREAD_PCT`, default 25 %) must pass on the real machine before launch, and its report is committed next to this page; see the runbook.

## Checking and recording

```bash
make benchmark                  # in the test container; saves docs/benchmarks/<date>-<label>.json
make benchmark-host             # on the Judge machine, as root, Judge stopped (see the runbook)
make benchmark-compare BEFORE=docs/benchmarks/a.json AFTER=docs/benchmarks/b.json
```

`BENCHMARK_ROUNDS` (default 10), `BENCHMARK_PARALLEL` (jobs together, default 4), `BENCHMARK_LABEL`, `BENCHMARK_MAX_SPREAD_PCT`, and for the host `INTEGRATION_BOX_BASE` and `INTEGRATION_CPUS` (the boxes and cores isolate is configured with) set the run. A report holds, for five programs, the minimum, median, 95th percentile and maximum time alone and together, plus the machine: CPU model, kernel, isolate, compilers and the pinned cores. `benchcompare` flags every program whose median moved by more than the threshold (default 10 %) and exits with status 1.

On a laptop the gate is expected to fail on the noisier programs; use `BENCHMARK_MAX_SPREAD_PCT=60` there to get a report.

Also run `isolate-check-environment` (the installer does): it lists the settings that add variance (swap, SMT, frequency scaling, transparent huge pages, address-space randomisation). We keep ASLR on (a security setting) and ask for the others in the runbook.
