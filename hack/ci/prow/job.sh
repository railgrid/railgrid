#!/usr/bin/env bash
# Body of every Prow job in .prow.yaml: `hack/ci/prow/job.sh <job>`.
#
# Each case runs the same commands as the GitHub Actions job it mirrors
# (.github/workflows/ci.yaml and e2e.yaml). When a workflow step changes,
# change the matching case here in the same pull request. The differences are
# the ones Prow forces:
#
#   - No job matrix and no artifacts between jobs. The module matrix is a loop
#     in one job, and every kind suite builds bin/ and the images itself.
#   - No service containers. The App Studio store tests start PostgreSQL from
#     the CI image.
#   - Selection (full / provider-ui / docs, hack/ci/README.md) is computed here
#     with the same classifier; a job the mode excludes exits 0 at once.
#
# Runs as the unprivileged `ci` user with passwordless sudo, from the repo
# root (hack/ci/prow/image/runner.sh).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

job="${1:?usage: job.sh <job>}"
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "${ARTIFACTS}"

selection="$(python3 hack/ci/prow/mode.py)"
mode="$(jq -r .mode <<<"${selection}")"
echo "== job=${job} mode=${mode}: $(jq -r .reason <<<"${selection}")"

# full_only: the job runs only when the whole tree is validated.
full_only() {
  if [[ "${mode}" != "full" ]]; then
    echo "== skipped: ${job} runs only in full mode"
    exit 0
  fi
}

# Provider binaries `//go:embed all:portal/dist`; the portals are built by the
# portals job, so give the embed a target instead of building them here.
stub_portal() {
  mkdir -p "$1/portal/dist" && touch "$1/portal/dist/.gitkeep"
}

# The e2e suites must link THIS commit's provider-sdk, not the published one;
# go.work is gitignored, so CI creates it (see e2e.yaml).
go_workspace() {
  go work init
  go work use . ./provider-sdk
  for d in providers/*/; do
    [[ -f "${d}go.mod" ]] && go work use "./${d}"
  done
  go work edit -json | grep -c DiskPath
}

# kind needs these on the HOST, and a job pod must not rewrite a node's
# sysctls behind the operator's back, so this only reports. Set them on the
# nodes (ops repo, cicd/README.md "Prow") if a suite fails to start kubelets.
kind_prereqs() {
  local watches instances
  watches="$(cat /proc/sys/fs/inotify/max_user_watches)"
  instances="$(cat /proc/sys/fs/inotify/max_user_instances)"
  echo "== inotify: max_user_watches=${watches} max_user_instances=${instances}"
  if (( watches < 524288 || instances < 512 )); then
    echo "== WARNING: below the 524288/512 the kind suites expect"
  fi
  docker info --format '== docker {{.ServerVersion}}, cgroup v{{.CgroupVersion}}, {{.Driver}}'
  echo "127.0.0.1 console.127.0.0.1.sslip.io" | sudo tee -a /etc/hosts >/dev/null
}

# On failure, keep what the Actions jobs upload: kind's own logs and *.log.
kind_logs_on_failure() {
  local rc=$?
  if (( rc != 0 )); then
    mkdir -p "${ARTIFACTS}/kind-logs"
    for cluster in $(kind get clusters 2>/dev/null); do
      kind export logs "${ARTIFACTS}/kind-logs/${cluster}" --name "${cluster}" || true
    done
    cp ./*.log "${ARTIFACTS}/" 2>/dev/null || true
  fi
  exit "${rc}"
}

# bin/ and the images the kind suites deploy with imagePullPolicy Never.
# proxy.golang.org intermittently drops HTTP/2 streams, hence the GODEBUG.
build_for_kind() {
  make build
  GO_BUILD_GODEBUG=http2client=0 make "$@"
  export PATH="${PWD}/bin:${PATH}"
}

case "${job}" in
  build)
    full_only
    for dir in . provider-sdk providers/*/ providers/infrastructure/dev-agent; do
      [[ -f "${dir}/go.mod" ]] || continue
      (cd "${dir}" && go mod download)
    done
    make build
    make verify-codegen
    ;;

  lint)
    full_only
    make lint
    stub_portal providers/app-studio
    make lint-app-studio-harness
    ;;

  # Runs in every mode, like the Actions `contracts` job.
  contracts)
    make verify-boilerplate
    make verify-design-docs
    make verify-tilt-browser-deployment
    (cd portal && npm ci --include=dev --no-audit --no-fund)
    make verify-portalkit
    make verify-ui-conformance
    make verify-provider-contract
    (cd provider-sdk/actions-node && npm test && npm run test:install)
    make test-app-studio-harness-scripts
    make test-tilt-checkout
    make test-tilt-app-studio-sequence
    python3 hack/scripts/verify-tilt-sandbox-default.test.py
    make verify-ci-selection
    make verify-workflows
    make verify-charts
    ;;

  # The root module: same package set as `make test`, under the race detector.
  test)
    full_only
    # shellcheck disable=SC2046
    go test -race -count=1 $(go list ./... | grep -v '/test/e2e')
    ;;

  # Every standalone module (the Actions test-modules matrix minus the root),
  # then the App Studio store suite against a real PostgreSQL.
  test-providers)
    full_only
    failed=()
    for module in provider-sdk providers/agents providers/app-studio providers/code \
                  providers/edges providers/infrastructure providers/infrastructure/dev-agent \
                  providers/kuery providers/quickstart; do
      echo "== ${module}"
      race="-race"
      # edges: TestSSHSessionTeardown races on its own serverConn (test-only
      # bug, see ci.yaml). Re-enable -race with the Actions matrix.
      [[ "${module}" == "providers/edges" ]] && race=""
      if [[ "${module}" == providers/* && "${module}" != "providers/infrastructure/dev-agent" ]]; then
        stub_portal "${module}"
      fi
      # shellcheck disable=SC2086
      (cd "${module}" && go vet ./... && go test ${race} -count=1 ./...) || failed+=("${module}")
    done

    echo "== providers/app-studio store against PostgreSQL"
    pgdata="$(mktemp -d)"
    initdb -D "${pgdata}" -U postgres --auth=trust >/dev/null
    pg_ctl -D "${pgdata}" -l "${ARTIFACTS}/postgres.log" -w \
      -o "-c listen_addresses=127.0.0.1 -c unix_socket_directories=${pgdata}" start
    createdb -h 127.0.0.1 -U postgres app_studio_test
    (cd providers/app-studio && \
      APP_STUDIO_TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:5432/app_studio_test?sslmode=disable' \
      go test -race -count=1 ./store) || failed+=("providers/app-studio/store (PostgreSQL)")
    pg_ctl -D "${pgdata}" -m fast stop >/dev/null || true

    if (( ${#failed[@]} )); then
      printf '== FAILED: %s\n' "${failed[@]}"
      exit 1
    fi
    ;;

  # Root portal (full mode) and the provider portals the selection names:
  # all of them in full mode, the affected ones in provider-ui mode.
  portals)
    if [[ "${mode}" == "docs" ]]; then
      echo "== skipped: docs-only change"
      exit 0
    fi
    if [[ "${mode}" == "full" ]]; then
      echo "== root portal"
      (cd portal && npm ci --no-audit --no-fund && npm test && npm run build)
    fi
    failed=()
    while IFS=$'\t' read -r provider run_test run_typecheck; do
      echo "== providers/${provider}/portal"
      (
        cd "providers/${provider}/portal"
        npm ci --no-audit --no-fund
        if [[ "${run_test}" == "true" ]]; then npm test; fi
        if [[ "${run_typecheck}" == "true" ]]; then npm run typecheck; fi
        npm run build
      ) || failed+=("${provider}")
    done < <(jq -r '."portal-matrix".include[] | [.provider, (.test // false), (.typecheck // false)] | @tsv' <<<"${selection}")
    if (( ${#failed[@]} )); then
      printf '== FAILED portal: %s\n' "${failed[@]}"
      exit 1
    fi
    ;;

  # Hub with embedded kcp + provider binaries as host processes. No Docker.
  # Sequential: the suites share the embedded kcp's fixed etcd port.
  e2e-providers)
    full_only
    go_workspace
    make e2e-infra-provider
    make e2e-provider
    make e2e-kuery-provider
    make e2e-agents-provider
    make e2e-edges
    ;;

  e2e-standalone)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    build_for_kind e2e-images
    make e2e-standalone-run E2E_TIMEOUT=50m E2E_FLAGS="-ssh-keepalive-duration=30s -keep-clusters"
    ;;

  e2e-ssh)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    build_for_kind e2e-hub-image
    make e2e-ssh-run E2E_TIMEOUT=30m E2E_FLAGS="-keep-clusters"
    ;;

  e2e-oidc)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    echo "127.0.0.1 dex.railgrid-system.svc.cluster.local" | sudo tee -a /etc/hosts >/dev/null
    helm repo add dexidp https://charts.dexidp.io
    helm repo update dexidp
    build_for_kind e2e-hub-image
    make e2e-oidc-run E2E_TIMEOUT=35m E2E_FLAGS="-keep-clusters"
    ;;

  e2e-external-kcp)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    helm repo add kcp-dev https://kcp-dev.github.io/helm-charts
    helm repo update kcp-dev
    build_for_kind e2e-hub-image
    make e2e-external-kcp-run E2E_TIMEOUT=70m E2E_FLAGS="-keep-clusters"
    ;;

  e2e-edges-connectivity)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    RAILGRID_E2E_ARTIFACT_DIR="${ARTIFACTS}/railgrid-e2e-edges-connectivity" \
      make e2e-edges-connectivity E2E_EDGES_CONN_TIMEOUT=20m
    ;;

  e2e-cli)
    full_only
    trap kind_logs_on_failure EXIT
    kind_prereqs
    RAILGRID_E2E_ARTIFACT_DIR="${ARTIFACTS}/railgrid-e2e-cli" \
      make e2e-cli E2E_CLI_TIMEOUT=25m
    ;;

  *)
    echo "unknown job: ${job}" >&2
    exit 2
    ;;
esac
