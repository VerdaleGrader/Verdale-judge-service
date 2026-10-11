# Running Judge in production (systemd)

Judge runs directly on the host, managed by systemd. Redis runs in Docker
Compose on the same machine, bound to `127.0.0.1`. The Judge machine holds
no database credentials, only a read-only R2 token for the test bucket.
Hardening and the full runbook are GRD-41; this is the minimum to run.

1. **Kernel**: Linux with cgroup v2 (`ls /sys/fs/cgroup/cgroup.controllers`). Turn off swap.
2. **isolate v2.7**: build from https://github.com/ioi/isolate (tag `v2.7`), `make install`, then enable the shipped units: `systemctl enable --now isolate.service` (it delegates `isolate.slice` and runs `isolate-cg-keeper`). Use the shipped config (`cg_root = auto:/run/isolate/cgroup`, `subid_user = isolate`).
3. **Toolchains** pinned in `config.json`: gcc/g++ 12.2.0, Python 3.11.2 (Debian 12). Judge refuses to start if a version differs.
4. **Redis**: `REDIS_PASSWORD=... docker compose -f docker-compose.redis.yml up -d`.
5. **Judge**: `make build`, copy `bin/verdale-judge` to `/usr/local/bin`, `config.json` to `/etc/verdale-judge/` (set `environment` to `production`, `log.format` to `json`, `storage.driver` to `s3`, `callback.allowed_url_prefixes` to Core's URL), and create `/etc/verdale-judge/judge.env` (mode 600):

   ```
   VERDALE_JUDGE_REDIS_ADDR=127.0.0.1:6379
   VERDALE_JUDGE_REDIS_PASSWORD=...
   VERDALE_JUDGE_AUTH_INBOUND_KEYS=core-2026-10:...
   VERDALE_JUDGE_CALLBACK_KEY_ID=judge-2026-10
   VERDALE_JUDGE_CALLBACK_SECRET=...
   VERDALE_JUDGE_STORAGE_S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
   VERDALE_JUDGE_STORAGE_S3_BUCKET=verdale-tests
   VERDALE_JUDGE_STORAGE_S3_ACCESS_KEY_ID=...
   VERDALE_JUDGE_STORAGE_S3_SECRET_ACCESS_KEY=...
   ```
6. `cp verdale-judge.service /etc/systemd/system/ && systemctl enable --now verdale-judge`.
7. Check `curl localhost:8081/ready` and scrape `localhost:8081/metrics`.
