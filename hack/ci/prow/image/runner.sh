#!/usr/bin/env bash
# Entry point of every Prow job: `runner.sh <command> [args...]`.
#
# Runs as root, which is what Prow starts the container as. It starts Docker
# when the job asked for it (label preset-dind-enabled, which sets
# DOCKER_IN_DOCKER_ENABLED and needs a privileged container), hands the
# checkout to the `ci` user, runs the command as that user, and cleans up.
set -euo pipefail

# Become a child subreaper before anything else. Prow's entrypoint is PID 1 in
# the job container and does not reap orphans, so a killed grandchild stays a
# zombie forever, and to kill(pid, 0) a zombie still exists: every "the process
# group was killed" test in the tree failed on the first cluster run. tini -s
# adopts and reaps everything below this script; -g forwards an abort's
# SIGTERM to the whole process group.
if [[ -z "${RUNNER_REAPER:-}" ]]; then
  export RUNNER_REAPER=1
  exec tini -s -g -- "$0" "$@"
fi

CI_USER=ci
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "${ARTIFACTS}"

dockerd_pid=""
cleanup() {
  if [[ -n "${dockerd_pid}" ]]; then
    docker ps -aq 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
    kill "${dockerd_pid}" 2>/dev/null || true
    wait "${dockerd_pid}" 2>/dev/null || true
    cp /var/log/dockerd.log "${ARTIFACTS}/dockerd.log" 2>/dev/null || true
  fi
}
trap cleanup EXIT

if [[ "${DOCKER_IN_DOCKER_ENABLED:-false}" == "true" ]]; then
  # Keep everything Docker starts (image builds, kind nodes) INSIDE this
  # container's cgroup, so the job's CPU and memory limits cover it.
  #
  # Kubernetes gives a privileged container the HOST cgroup namespace:
  # /sys/fs/cgroup here is the node's root, and a dockerd left to its defaults
  # creates /docker there, outside the pod. On the first cluster runs that is
  # where every build and kind node ran, unlimited, next to the dev platform.
  # So: find our own cgroup, and make dockerd parent its containers under it.
  #
  # cgroup v2 forbids a cgroup that holds processes from delegating
  # controllers to children, so first move what is in ours into a leaf
  # (`init`) and enable the controllers. Same step as docker's own dind
  # entrypoint, applied to our cgroup instead of the namespace root. With a
  # private cgroup namespace (plain `docker run --privileged`) own_cgroup is
  # empty and this reduces to exactly that upstream behaviour.
  own_cgroup="$(sed -n 's/^0:://p' /proc/self/cgroup)"
  own_cgroup="${own_cgroup%/}"
  cgroup_dir="/sys/fs/cgroup${own_cgroup}"
  if [[ -f "${cgroup_dir}/cgroup.controllers" ]]; then
    mkdir -p "${cgroup_dir}/init"
    xargs -rn1 < "${cgroup_dir}/cgroup.procs" > "${cgroup_dir}/init/cgroup.procs" 2>/dev/null || true
    sed -e 's/ / +/g' -e 's/^/+/' < "${cgroup_dir}/cgroup.controllers" \
      > "${cgroup_dir}/cgroup.subtree_control" 2>/dev/null || true
  fi
  # The pod network is a VXLAN overlay with an MTU below 1500. Containers on a
  # default 1500 bridge would have large packets dropped; kind copies this
  # value onto its own network.
  dockerd --cgroup-parent="${own_cgroup}/docker" --exec-opt native.cgroupdriver=cgroupfs \
    --mtu="${DOCKER_MTU:-1400}" > /var/log/dockerd.log 2>&1 &
  dockerd_pid=$!
  for _ in $(seq 1 60); do
    docker info >/dev/null 2>&1 && break
    sleep 1
  done
  if ! docker info >/dev/null 2>&1; then
    echo "runner: dockerd did not come up within 60s" >&2
    tail -50 /var/log/dockerd.log >&2 || true
    exit 1
  fi
  echo "runner: docker $(docker version --format '{{.Server.Version}}') is up"
fi

# clonerefs wrote the checkout as root.
chown -R "${CI_USER}:" "${PWD}" "${ARTIFACTS}"

rc=0
# -E keeps Prow's job environment (JOB_NAME, PULL_*, ARTIFACTS, ...); PATH is
# passed explicitly because sudo's secure_path would replace it.
sudo -E -H -u "${CI_USER}" env "PATH=${PATH}" "$@" || rc=$?
exit "${rc}"
