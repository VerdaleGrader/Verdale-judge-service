# Running Judge in production (systemd)

The full procedure, with the reasons, is in [docs/runbook.md](../../docs/runbook.md).
In short, on a Debian 12 machine with cgroup v2:

```bash
sudo deploy/install.sh check     # is the machine fit?
sudo CALLBACK_URL_PREFIX=https://<core>/internal/judge/ deploy/install.sh install
```

Then create `/etc/verdale-judge/judge.env` (root, mode 0600). Judge starts only
after you have read the runbook's "Prove it" step:

```
VERDALE_JUDGE_REDIS_ADDR=127.0.0.1:6379
VERDALE_JUDGE_REDIS_PASSWORD=...
VERDALE_JUDGE_AUTH_INBOUND_KEYS=core-2026-10:...
VERDALE_JUDGE_CALLBACK_KEY_ID=judge-2026-10
VERDALE_JUDGE_CALLBACK_SECRET=...
VERDALE_JUDGE_STORAGE_S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
VERDALE_JUDGE_STORAGE_S3_BUCKET=verdale-tests
VERDALE_JUDGE_STORAGE_S3_ACCESS_KEY_ID=...        # read-only token for the tests bucket
VERDALE_JUDGE_STORAGE_S3_SECRET_ACCESS_KEY=...
```

`REDIS_PASSWORD=... docker compose -f deploy/systemd/docker-compose.redis.yml up -d`
starts Redis on `127.0.0.1`. Files here: `verdale-judge.service` (the unit),
`isolate.slice.d/limits.conf` (ceiling for all sandboxes together).
