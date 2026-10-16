#!/usr/bin/env bash
# Sets up a Judge machine (Debian 12, bare metal or a VM with dedicated cores).
#
#   sudo deploy/install.sh check       look only: is this machine fit to judge code?
#   sudo deploy/install.sh install     do everything below, step by step, safe to run again
#
# install: system user, isolate (pinned tag and commit), the pinned compilers,
# directories, config.json for production, one core per box, the systemd unit,
# limits for the sandboxes, and (with FIREWALL=1) the firewall. It never starts
# Judge: do the checks in docs/runbook.md first, then `systemctl enable --now`.
#
# Settings (environment):
#   VERDALE_JUDGE_BINARY   the built binary (default bin/verdale-judge; `make build`)
#   SANDBOX_CPUSET         cores for sandboxes, e.g. "2-7" (default: one thread per
#                          physical core, minus the first core and its sibling)
#   CALLBACK_URL_PREFIX    Core's callback URL prefix, e.g. https://api.example.com/internal/judge/
#   FIREWALL=1             also install the firewall (needs /etc/verdale-judge/firewall.env)
#   SKIP_SYSTEMD=1         do not touch systemd or nftables (used to test this script in a container)
#   ALLOW_SWAP=1           continue although swap is on
set -euo pipefail

# --- What Judge is pinned to. Change these together with config.json. -------
ISOLATE_TAG=v2.7
ISOLATE_COMMIT=8f185bb37f3f23e29b33b0c7727c91c13429abe3
GCC_VERSION=12.2.0
PYTHON_VERSION=3.11.2
DEBIAN_VERSION=12

SERVICE_USER=verdale-judge
CONFIG_DIR=/etc/verdale-judge
STATE_DIR=/var/lib/verdale-judge
REPO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
FAILED=0

say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok    %s\n' "$*"; }
warn() { printf '   WARN  %s\n' "$*" >&2; }
fail() { printf '   FAIL  %s\n' "$*" >&2; FAILED=1; }
die()  { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

online_cpus() { nproc --all; }

# One thread per physical core, without the first core and its sibling.
default_cpuset() {
  python3 - <<'PY'
import glob, os, re
def parse(text):
    out = set()
    for part in text.strip().split(","):
        if "-" in part:
            a, b = part.split("-"); out.update(range(int(a), int(b) + 1))
        elif part:
            out.add(int(part))
    return out
cores = sorted(int(re.search(r"cpu(\d+)$", p).group(1)) for p in glob.glob("/sys/devices/system/cpu/cpu[0-9]*"))
reserved = set()
chosen = []
for core in cores:
    path = f"/sys/devices/system/cpu/cpu{core}/topology/thread_siblings_list"
    siblings = parse(open(path).read()) if os.path.exists(path) else {core}
    if core != min(siblings):
        continue                      # a second hyper-thread of a core already counted
    if not reserved:
        reserved = siblings           # the first core (and its sibling) stay with Judge, Redis and the OS
        continue
    chosen.append(core)
def compress(values):
    values = sorted(values); out = []; start = prev = None
    for v in values:
        if start is None: start = prev = v
        elif v == prev + 1: prev = v
        else: out.append((start, prev)); start = prev = v
    if start is not None: out.append((start, prev))
    return ",".join(str(a) if a == b else f"{a}-{b}" for a, b in out)
print(compress(chosen))
PY
}

# --- Checks (also run by `install`) -----------------------------------------
preflight() {
  say "Checking the machine"
  if [ "$(id -u)" -eq 0 ]; then ok "running as root"; else fail "run as root (sudo)"; fi

  . /etc/os-release 2>/dev/null || true
  if [ "${ID:-}" = debian ] && [ "${VERSION_ID:-}" = "$DEBIAN_VERSION" ]; then ok "Debian $DEBIAN_VERSION"
  else fail "Debian $DEBIAN_VERSION is required (the compilers are pinned to its packages), found ${PRETTY_NAME:-unknown}"; fi

  if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = cgroup2fs ]; then ok "cgroup v2 is mounted"
  else fail "cgroup v2 is not mounted at /sys/fs/cgroup (boot with systemd.unified_cgroup_hierarchy=1)"; fi
  local controllers; controllers=$(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null || true)
  for controller in memory pids cpuset; do
    case " $controllers " in *" $controller "*) ok "cgroup controller $controller" ;; *) fail "cgroup controller $controller is not available" ;; esac
  done

  if [ -n "$(swapon --show --noheadings 2>/dev/null)" ]; then
    if [ "${ALLOW_SWAP:-}" = 1 ]; then warn "swap is on (ALLOW_SWAP=1)"; else fail "swap is on: swapoff -a and remove it from /etc/fstab, or ALLOW_SWAP=1 (memory limits and times are not reliable with swap)"; fi
  else ok "swap is off"; fi

  local cpus; cpus=$(online_cpus)
  if [ "$cpus" -ge 4 ]; then ok "$cpus CPUs"; else fail "$cpus CPUs: at least 4 are needed (1 for the system, 3 for sandboxes)"; fi

  local smt=/sys/devices/system/cpu/smt/active
  if [ -r "$smt" ] && [ "$(cat "$smt")" = 1 ]; then warn "SMT (hyper-threading) is on: sandboxes will use one thread per physical core only; 'nosmt' on the kernel command line is better"; fi

  for tool in gcc g++ python3; do
    if command -v "$tool" >/dev/null; then :; else warn "$tool is not installed yet (install will add it)"; fi
  done
  if command -v gcc >/dev/null; then
    [ "$(gcc -dumpfullversion)" = "$GCC_VERSION" ] && ok "gcc $GCC_VERSION" || fail "gcc is $(gcc -dumpfullversion), pinned $GCC_VERSION"
  fi
  if command -v python3 >/dev/null; then
    [ "$(python3 -c 'import platform; print(platform.python_version())')" = "$PYTHON_VERSION" ] && ok "python $PYTHON_VERSION" || fail "python is $(python3 --version), pinned $PYTHON_VERSION"
  fi
  if [ -x /usr/local/bin/isolate ]; then
    /usr/local/bin/isolate --version | head -1 | grep -q "2\.7" && ok "isolate 2.7" || fail "isolate is not 2.7"
    [ -u /usr/local/bin/isolate ] && ok "isolate is setuid root" || fail "isolate is not setuid root"
  fi
  return 0
}

install_packages() {
  say "Installing packages"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends \
    gcc g++ libc6-dev python3 make git ca-certificates pkg-config libcap-dev libseccomp-dev libsystemd-dev \
    util-linux nftables >/dev/null
  [ "$(gcc -dumpfullversion)" = "$GCC_VERSION" ] || die "gcc is $(gcc -dumpfullversion) but $GCC_VERSION is pinned: update the pins in this script, config.json and the benchmark together"
  [ "$(g++ -dumpfullversion)" = "$GCC_VERSION" ] || die "g++ is not $GCC_VERSION"
  [ "$(python3 -c 'import platform; print(platform.python_version())')" = "$PYTHON_VERSION" ] || die "python is not $PYTHON_VERSION"
  ok "gcc/g++ $GCC_VERSION and python $PYTHON_VERSION"
}

install_users_and_dirs() {
  say "Users and directories"
  id "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$SERVICE_USER"
  # The uids sandboxes run as: a block that belongs to the user "isolate" alone.
  id isolate >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin isolate
  grep -q '^isolate:' /etc/subuid 2>/dev/null || echo 'isolate:200000:65536' >> /etc/subuid
  grep -q '^isolate:' /etc/subgid 2>/dev/null || echo 'isolate:200000:65536' >> /etc/subgid
  install -d -m 0750 -o root -g "$SERVICE_USER" "$CONFIG_DIR"
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$STATE_DIR" "$STATE_DIR/work" "$STATE_DIR/cache"
  install -d -m 0700 -o root -g root "$STATE_DIR/redis"
  ok "user $SERVICE_USER, $STATE_DIR, $CONFIG_DIR"
}

install_isolate() {
  say "isolate $ISOLATE_TAG"
  local src=/usr/local/src/isolate
  if [ ! -d "$src/.git" ]; then git clone -q --branch "$ISOLATE_TAG" https://github.com/ioi/isolate.git "$src"; fi
  git -C "$src" fetch -q --tags
  git -C "$src" checkout -q "$ISOLATE_TAG"
  local commit; commit=$(git -C "$src" rev-parse HEAD)
  [ "$commit" = "$ISOLATE_COMMIT" ] || die "isolate $ISOLATE_TAG is commit $commit, expected $ISOLATE_COMMIT: the tag was moved or the pin is wrong"
  make -s -C "$src" install >/dev/null
  ok "built and installed commit ${commit:0:12} (setuid root)"
}

# Writes isolate's configuration with one core for each box.
configure_isolate() {
  say "isolate configuration: one core for each box"
  local base=$1 cpuset=$2
  python3 - "$base" "$cpuset" "$REPO_DIR/deploy/host/isolate.cf" /usr/local/etc/isolate <<'PY'
import sys
base, cpuset, template, target = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
cores = []
for part in cpuset.split(","):
    a, _, b = part.partition("-"); cores += range(int(a), int(b or a) + 1)
marker = "\n# --- pinned by install.sh ---"
text = open(template).read().rstrip("\n").split(marker)[0] + marker + "\n"
text += "".join(f"box{base + index}.cpus = {core}\n" for index, core in enumerate(cores))
open(target, "w").write(text)
print(f"   ok    boxes {base}..{base + len(cores) - 1} on cores {cpuset}")
PY
}

merge_config() {
  say "$CONFIG_DIR/config.json"
  local cpuset=$1 prefix=${CALLBACK_URL_PREFIX:-}
  python3 - "$REPO_DIR/config.json" "$CONFIG_DIR/config.json" "$cpuset" "$prefix" "$STATE_DIR" <<'PY'
import json, sys
source, target, cpuset, prefix, state = sys.argv[1:6]
config = json.load(open(source))
config["environment"] = "production"
config["log"]["format"] = "json"
config["redis"]["addr"] = "127.0.0.1:6379"
config["storage"]["driver"] = "s3"
config["storage"]["local_dir"] = ""
config["sandbox"]["cpuset"] = cpuset
config["sandbox"]["work_dir"] = f"{state}/work"
config["cache"]["dir"] = f"{state}/cache"
config["worker"]["concurrency"] = 0          # one job for each core of the cpuset
if prefix:
    config["callback"]["allowed_url_prefixes"] = [prefix]
elif config["callback"]["allowed_url_prefixes"] and "mockcore" in config["callback"]["allowed_url_prefixes"][0]:
    config["callback"]["allowed_url_prefixes"] = []   # must be set: CALLBACK_URL_PREFIX or VERDALE_JUDGE_CALLBACK_ALLOWED_URL_PREFIXES
json.dump(config, open(target, "w"), indent=2); open(target, "a").write("\n")
PY
  chown root:"$SERVICE_USER" "$CONFIG_DIR/config.json"; chmod 0640 "$CONFIG_DIR/config.json"
  if [ -z "$prefix" ]; then warn "CALLBACK_URL_PREFIX was not given: set callback.allowed_url_prefixes in $CONFIG_DIR/config.json to Core's callback URL"; fi
  ok "written (environment production, storage s3, no secrets)"
}

install_binary_and_units() {
  say "Judge binary and systemd units"
  local binary=${VERDALE_JUDGE_BINARY:-$REPO_DIR/bin/verdale-judge}
  [ -x "$binary" ] || die "no binary at $binary: run 'make build' on a build machine and copy it, or set VERDALE_JUDGE_BINARY"
  install -m 0755 -o root -g root "$binary" /usr/local/bin/verdale-judge
  [ "${SKIP_SYSTEMD:-}" = 1 ] && { ok "binary installed; systemd skipped"; return; }

  local cpuset=$1 reserved
  reserved=$(python3 - "$cpuset" "$(online_cpus)" <<'PY'
import sys
used = set()
for part in sys.argv[1].split(","):
    a, _, b = part.partition("-"); used.update(range(int(a), int(b or a) + 1))
rest = sorted(set(range(int(sys.argv[2]))) - used)
out, start, prev = [], None, None
for v in rest:
    if start is None: start = prev = v
    elif v == prev + 1: prev = v
    else: out.append((start, prev)); start = prev = v
if start is not None: out.append((start, prev))
print(",".join(str(a) if a == b else f"{a}-{b}" for a, b in out))
PY
)
  sed "s/^CPUAffinity=.*/CPUAffinity=${reserved}/" "$REPO_DIR/deploy/systemd/verdale-judge.service" > /etc/systemd/system/verdale-judge.service
  install -d /etc/systemd/system/isolate.slice.d
  install -m 0644 "$REPO_DIR/deploy/systemd/isolate.slice.d/limits.conf" /etc/systemd/system/isolate.slice.d/limits.conf
  systemctl daemon-reload
  systemctl enable --now isolate.service
  ok "isolate.service running; verdale-judge.service installed (not started), Judge, Redis and the OS keep cores $reserved"
}

install_firewall() {
  [ "${SKIP_SYSTEMD:-}" = 1 ] && return
  [ "${FIREWALL:-}" = 1 ] || { warn "firewall not installed (FIREWALL=1 to install it; see deploy/firewall/judge.nft)"; return; }
  say "Firewall"
  local env_file=$CONFIG_DIR/firewall.env
  [ -r "$env_file" ] || die "$env_file is missing: copy deploy/firewall/firewall.env.example and fill it in"
  # shellcheck disable=SC1090
  . "$env_file"
  local rendered; rendered=$(mktemp)
  sed -e "s|^define CORE_ADDRESS = .*|define CORE_ADDRESS = { ${CORE_ADDRESS:?} }|" \
      -e "s|^define ADMIN_CIDR = .*|define ADMIN_CIDR = { ${ADMIN_CIDR:?} }|" \
      -e "s|^define METRICS_CIDR = .*|define METRICS_CIDR = { ${METRICS_CIDR:-${ADMIN_CIDR}} }|" \
      "$REPO_DIR/deploy/firewall/judge.nft" > "$rendered"
  nft -c -f "$rendered" || die "the firewall rules do not load"
  warn "applying the firewall: SSH stays open only for $ADMIN_CIDR. Keep this session open and test a second one."
  install -m 0644 "$rendered" /etc/nftables.conf
  systemctl enable --now nftables
  nft -f /etc/nftables.conf
  ok "firewall loaded"
}

variability_report() {
  say "Sources of timing variability (isolate-check-environment)"
  if command -v isolate-check-environment >/dev/null; then isolate-check-environment || true; else warn "isolate-check-environment is not installed"; fi
}

case "${1:-}" in
  check)
    preflight
    [ "$FAILED" -eq 0 ] || die "the machine is not ready (see FAIL above)"
    command -v isolate-check-environment >/dev/null && variability_report
    ok "ready for: sudo deploy/install.sh install"
    ;;
  install)
    preflight
    [ "$FAILED" -eq 0 ] || die "fix the FAIL lines above first"
    CPUSET=${SANDBOX_CPUSET:-$(default_cpuset)}
    [ -n "$CPUSET" ] || die "no cores are left for sandboxes"
    BASE=$(python3 -c "import json; print(json.load(open('$REPO_DIR/config.json'))['sandbox']['box_id_base'])")
    say "Sandboxes will use cores $CPUSET, from box id $BASE"
    install_packages
    install_users_and_dirs
    install_isolate
    configure_isolate "$BASE" "$CPUSET"
    merge_config "$CPUSET"
    install_binary_and_units "$CPUSET"
    install_firewall
    variability_report
    preflight
    [ "$FAILED" -eq 0 ] || die "something is wrong after the install (see FAIL above)"
    cat <<MSG

Installed. Not started. Next (docs/runbook.md, "New machine"):
  1. write $CONFIG_DIR/judge.env (mode 0600, root) with the secrets of docs/secrets.md (Core repo)
  2. start Redis:  REDIS_PASSWORD=... docker compose -f deploy/systemd/docker-compose.redis.yml up -d
  3. run the checks and the benchmark, then: systemctl enable --now verdale-judge
MSG
    ;;
  *)
    sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//; /^set -euo/d'
    exit 2
    ;;
esac
