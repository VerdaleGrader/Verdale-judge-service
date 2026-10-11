#!/usr/bin/env bash
# End-to-end resilience checks against the local stack (make up):
#   1. Judge is killed (SIGKILL) while judging: the job is reclaimed after
#      the idle timeout and its result still reaches Core.
#   2. Judge stops gracefully with jobs queued and Redis restarts: the
#      jobs survive (AOF) and are judged when Judge comes back.
#   3. Core is down when results are ready: results wait in the outbox and
#      are delivered once Core is back.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

results=tmp/results
submit() { go run ./cmd/submit -problem examples/aplusb -source "$1" -kind "${2:-submission}" -storage dev/storage/tests | head -1 | awk '{print $2}'; }
wait_for_result() {
  local job_id="$1" timeout="$2"
  for _ in $(seq 1 "$timeout"); do
    [ -f "$results/$job_id.json" ] && { grep -o '"verdict": "[A-Z]*"' "$results/$job_id.json" | head -1; return 0; }
    sleep 1
  done
  echo "no result for $job_id after ${timeout}s" >&2
  return 1
}
wait_ready() {
  for _ in $(seq 1 60); do curl -sf http://localhost:8081/ready >/dev/null && return 0; sleep 1; done
  echo "judge did not become ready" >&2; return 1
}

echo "== 1. kill Judge while it is judging"
wait_ready
# A long verify job: 4 tests at the 1 s time limit each.
job=$(submit examples/aplusb/solutions/tle.c verify)
sleep 1.5
docker compose kill -s SIGKILL judge >/dev/null
echo "judge killed mid-job ($job); starting it again"
docker compose up -d judge >/dev/null
wait_ready
wait_for_result "$job" 90

echo "== 2. stop Judge gracefully with jobs queued, then restart Redis"
queued=()
for _ in 1 2 3 4; do queued+=("$(submit examples/aplusb/solutions/tle.py verify)"); done
sleep 1
# Graceful stop: jobs already running finish, queued jobs stay in Redis.
docker compose stop -t 120 judge >/dev/null
docker compose restart redis >/dev/null
docker compose start judge >/dev/null
wait_ready
for job_id in "${queued[@]}"; do wait_for_result "$job_id" 120; done

echo "== 3. Core is down when the result is ready"
docker compose stop mockcore >/dev/null
job_c=$(submit examples/aplusb/solutions/wa.cpp)
sleep 5
[ ! -f "$results/$job_c.json" ] || { echo "result arrived while Core was down?" >&2; exit 1; }
docker compose start mockcore >/dev/null
wait_for_result "$job_c" 120

echo "All resilience checks passed"
