# Judge machine runbook (GRD-41)

Judge runs untrusted code, so the machine is built to be wiped and rebuilt.
Nothing on it is precious: jobs are in Redis (and Core can re-queue them), tests
are in R2, and there is **no database credential** on it.

## What the machine is

| Part | How |
| --- | --- |
| Judge | `verdale-judge.service`, user `verdale-judge` (no shell, no home, no sudo), `MemoryMax=2G`, `TasksMax=2048`, `ProtectSystem=strict`, `PrivateTmp`, `ProtectHome` |
| isolate | v2.7, pinned by tag **and commit**; setuid root (the only root code Judge uses); `isolate.service` keeps the cgroup tree; every box has one core (`boxN.cpus`) |
| Sandboxes | `isolate.slice`: a ceiling for all boxes together (`MemoryMax=85%`, no swap); each box has its own limits on top |
| Cores | `sandbox.cpuset` for sandboxes (one thread per physical core); the rest, with `CPUAffinity`, for Judge, Redis and the OS |
| Redis | Docker Compose, `127.0.0.1` only, password, AOF |
| Network | nftables: inbound 8081 from Core (and the monitoring server), SSH from admins; outbound HTTPS, DNS, NTP (`deploy/firewall/judge.nft`) |
| Secrets | `/etc/verdale-judge/judge.env`, root only, mode 0600; list and rotation in Core's `docs/secrets.md` |

Why no `NoNewPrivileges`, capability bounding set or `ProtectControlGroups` in
the unit: isolate is setuid root and needs all three to make namespaces and
cgroups. The unit hardens everything isolate does not need.

## New machine

Needs: Debian 12, cgroup v2, at least 4 cores, swap off. Do this in order.

1. **Look first**: `sudo deploy/install.sh check`. It lists what is wrong and what adds timing variance (SMT, frequency scaling, transparent huge pages). Fix the FAIL lines. For the rest: `echo never > /sys/kernel/mm/transparent_hugepage/enabled` (persist with `transparent_hugepage=never` on the kernel command line), `cpupower frequency-set -g performance`, `nosmt` if the provider allows.
2. **Build the binary** on a build machine (`make build`, Go 1.24) and copy `bin/verdale-judge` and this repository's `deploy/`, `config.json` to the machine. Nothing is built from source on the Judge machine except isolate.
3. **Install**: `sudo CALLBACK_URL_PREFIX=https://<core>/internal/judge/ deploy/install.sh install` (add `SANDBOX_CPUSET=2-7` to choose the cores). It creates the user, builds isolate at the pinned commit, pins the compilers (it stops if `gcc` is not 12.2.0 or Python not 3.11.2), writes `config.json` (production, S3, no secrets), writes the `boxN.cpus` lines, installs the unit and the slice limits, and starts only `isolate.service`. It is safe to run again.
4. **Secrets**: create `/etc/verdale-judge/judge.env` (mode 0600, owner root) with the entries of the example in `deploy/systemd/README.md`. The R2 token must be **read-only on the tests bucket**. No `DATABASE_URL`, no Core session secret.
5. **Redis**: `REDIS_PASSWORD=... docker compose -f deploy/systemd/docker-compose.redis.yml up -d`.
6. **Firewall**: copy `deploy/firewall/firewall.env.example` to `/etc/verdale-judge/firewall.env`, fill it in, run `sudo FIREWALL=1 deploy/install.sh install`. Keep your SSH session open and open a second one before closing it.
7. **Prove it** (Judge still stopped):
   - sandbox attacks: `sudo INTEGRATION_BOX_BASE=100 INTEGRATION_CPUS=<cpuset> go test -count=1 -tags integration -v ./integration/...` (from a test binary built with `go test -c -tags integration ./integration` if Go is not installed here);
   - timing: `sudo INTEGRATION_BOX_BASE=100 INTEGRATION_CPUS=<cpuset> BENCHMARK_LABEL=<hostname> make benchmark-host`, then commit the report in `docs/benchmarks/` (docs/timing.md);
   - `sudo -u verdale-judge sh -c 'cat /etc/shadow; ls /root'` must be refused, and `getent passwd` must show no login shell for `verdale-judge`.
8. **Start**: `systemctl enable --now verdale-judge`, then `curl localhost:8081/ready` (the startup probe has already refused to start if a box was not on its core), scrape `/metrics`, and send one job from Core's staging.

Record in the launch checklist (M5): date, commit, who ran step 7, report file.

## Update the runtime (compiler, Python, isolate, kernel)

A change here changes measured times and possibly verdicts, so it is a release.

1. Announce a window; `systemctl stop verdale-judge` waits for running jobs (up to `worker.shutdown_timeout_seconds`) and leaves queued jobs in Redis.
2. Update the pins together: `GCC_VERSION`/`PYTHON_VERSION`/`ISOLATE_*` in `deploy/install.sh`, `languages.*.expected_version` in `config.json`, and the `Dockerfile` (CI uses it). Judge refuses to start on a mismatch, so a forgotten place is caught.
3. `sudo deploy/install.sh install`, then step 7 of "New machine" again, and `make benchmark-compare BEFORE=<old report> AFTER=<new report>`. A median that moves by more than 10 % means problems with limits near the edge need a look (docs/timing.md).
4. `systemctl start verdale-judge`; watch `judge_jobs_completed_total` and `judge_system_errors_total`.

Roll back by re-installing the previous commit and pins; the old binary stays in the build artefacts.

## When something is wrong

| Symptom | Look at | Do |
| --- | --- | --- |
| Judge will not start, log says "refusing to start" | `journalctl -u verdale-judge -n 50` | The message names the cause: cgroup v2 / controller missing, isolate not runnable, a box not on its core (`box<N>.cpus` line missing: run `install.sh install`), a compiler version mismatch, a bad config (all problems listed at once). |
| `/ready` fails | `curl localhost:8081/ready` | Redis down: `docker compose -f deploy/systemd/docker-compose.redis.yml ps`, `docker logs`. |
| Queue grows (`judge_queue_depth`, `judge_queue_oldest_job_age_seconds`) | `systemctl status`, `journalctl` | Workers busy: check `worker.concurrency`/cpuset; one stuck job is reclaimed after `queue.reclaim_idle_seconds`. Judge down: restart it, jobs wait in Redis. |
| `judge_queue_dead_letter_depth` > 0 | `redis-cli XRANGE judge:jobs:dead - +` | A job failed `queue.max_deliveries` times; Core was told ISE and can re-judge. Find the cause in the logs by job id before re-queueing. |
| `judge_outbox_pending` / `judge_outbox_dead` grow | logs of `callback` | Core unreachable or rejecting signatures (key id unknown, clock off by more than 300 s): fix, results are retried for 24 h. |
| Disk full | `df -h /var/lib/verdale-judge /var/local/lib/isolate` | The cache (`cache.max_bytes`) cleans itself; stale boxes: `isolate --cg --box-id=<n> --cleanup`; Judge cleans boxes at every run. |
| Times drift | `make benchmark-host` | Compare with the committed report; look at `isolate-check-environment`. |
| Suspected escape (odd processes, files, outbound traffic, a verdict that cannot be explained) | | **Do not clean up. Isolate the machine** (stop the service, firewall to admin only), keep its disk, **rebuild** from "New machine" on a fresh machine, and rotate every secret that was on it (Core's `docs/secrets.md`): the HMAC keys and the Redis and R2 read-only credentials. Judge never held database credentials. Jobs in Redis are not trusted after an incident: start from an empty Redis; unanswered submissions become ISE in Core and can be re-checked. |
| Machine lost | | Build a new one (steps 1 to 8). Queued jobs lived only in Redis: submissions that Judge never answers become ISE in Core after `DISPATCH_GIVE_UP_MS` and can be re-checked. |

## Day to day

- Logs: `journalctl -u verdale-judge` (JSON); Prometheus at `:8081/metrics` from the monitoring server only.
- Reload the config: edit `/etc/verdale-judge/config.json`, then `systemctl restart verdale-judge` (it drains first).
- Changing the number of cores for sandboxes: set `SANDBOX_CPUSET` and run `deploy/install.sh install` again (it rewrites the `boxN.cpus` lines and the config), then restart.
