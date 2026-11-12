#!/usr/bin/env bash
# Entry point of every Prow job: `runner.sh <command> [args...]`.
#
# Runs as root, which is what Prow starts the container as. It starts Docker
# when the job asked for it (label preset-dind-enabled, which sets
# DOCKER_IN_DOCKER_ENABLED and needs a privileged container), hands the
# checkout to the `ci` user, runs the command as that user, and cleans up.
set -euo pipefail

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
  # cgroup v2: a cgroup that has processes cannot delegate controllers to
  # children, and dockerd (then kind's kubelets) need them. Move everything in
  # this container's root cgroup into a leaf first. Same step as docker's own
  # dind entrypoint. This is the container's private cgroup namespace; the
  # host's /sys/fs/cgroup is deliberately not mounted into the pod.
  if [[ -f /sys/fs/cgroup/cgroup.controllers ]]; then
    mkdir -p /sys/fs/cgroup/init
    xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
      > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
  fi
  # The pod network is a VXLAN overlay with an MTU below 1500. Containers on a
  # default 1500 bridge would have large packets dropped; kind copies this
  # value onto its own network.
  dockerd --mtu="${DOCKER_MTU:-1400}" > /var/log/dockerd.log 2>&1 &
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
