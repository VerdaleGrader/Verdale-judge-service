#!/bin/sh
# Prepares cgroup v2 for isolate inside a privileged container started with
# a private cgroup namespace (--privileged --cgroupns=private).
#
# cgroup v2 forbids processes in a cgroup that delegates controllers to its
# children, so every process is first moved out of the namespace root into
# "init", then memory/pids/cpuset are enabled for the "isolate" subtree.
set -eu

CGROUP=/sys/fs/cgroup
if [ ! -f "$CGROUP/cgroup.controllers" ]; then
  echo "judge-entrypoint: cgroup v2 is not mounted at $CGROUP" >&2
  exit 1
fi
if [ ! -d "$CGROUP/isolate" ]; then
  mkdir -p "$CGROUP/init"
  while read -r pid; do
    echo "$pid" > "$CGROUP/init/cgroup.procs" 2>/dev/null || true
  done < "$CGROUP/cgroup.procs"
  echo "+cpuset +memory +pids" > "$CGROUP/cgroup.subtree_control"
  mkdir -p "$CGROUP/isolate"
  echo "+cpuset +memory +pids" > "$CGROUP/isolate/cgroup.subtree_control"
fi
mkdir -p /run/isolate/locks /var/local/lib/isolate
exec "$@"
