#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

set -euo pipefail

: "${FAROS_E2E_TILT_RUNTIME_KUBECONFIG:?FAROS_E2E_TILT_RUNTIME_KUBECONFIG is required}"
: "${FAROS_TERRAFORM_KIND_CLUSTER_NAME:?FAROS_TERRAFORM_KIND_CLUSTER_NAME is required}"
: "${INFRAKUBE_COMMIT:?INFRAKUBE_COMMIT is required}"
: "${CONTROLLER_IMAGE:?CONTROLLER_IMAGE is required}"
: "${TASK_IMAGE:?TASK_IMAGE is required}"

INFRAKUBE_REPOSITORY="${INFRAKUBE_REPOSITORY:-https://github.com/cwilhit/infrakube-multicluster.git}"
BUILD_CACHE_ROOT="${CODEX_BUILD_CACHE_ROOT:-/var/tmp/codex-build}"
RUNTIME_KUBECONFIG="${FAROS_E2E_TILT_RUNTIME_KUBECONFIG}"
KIND_CLUSTER_NAME="${FAROS_TERRAFORM_KIND_CLUSTER_NAME}"

for tool in docker git kind kubectl sed; do
  command -v "${tool}" >/dev/null || {
    echo "${tool} is required for the Terraform through Infrakube demonstration" >&2
    exit 1
  }
done

mkdir -p "${BUILD_CACHE_ROOT}"
source_dir="$(mktemp -d "${BUILD_CACHE_ROOT}/faros-infrakube.XXXXXX")"
cleanup() {
  rm -rf -- "${source_dir}"
}
trap cleanup EXIT HUP INT TERM

echo ">>> cloning Infrakube fork and checking out pinned commit ${INFRAKUBE_COMMIT}"
git clone --quiet "${INFRAKUBE_REPOSITORY}" "${source_dir}"
git -C "${source_dir}" checkout --quiet --detach "${INFRAKUBE_COMMIT}"
actual_commit="$(git -C "${source_dir}" rev-parse HEAD)"
if [[ "${actual_commit}" != "${INFRAKUBE_COMMIT}" ]]; then
  echo "Infrakube checkout is ${actual_commit}, want ${INFRAKUBE_COMMIT}" >&2
  exit 1
fi

# Git records only the executable bit, then applies the caller's umask while
# checking files out. A restrictive development umask can therefore produce
# 0700 or 0600 scripts; Docker COPY preserves that mode and the images run as
# non-root users, making their root-owned entrypoints unreadable. Normalize
# only the controller/task scripts that the pinned Dockerfiles copy and run.
chmod 755 \
  "${source_dir}/build/scripts/infrakube-entrypoint" \
  "${source_dir}/build/scripts/user_setup" \
  "${source_dir}/task-container-build-tools/scripts/entrypoint-wrapper.sh" \
  "${source_dir}/task-container-build-tools/scripts/extract-terraform.sh" \
  "${source_dir}/task-container-build-tools/scripts/extract-tofu.sh" \
  "${source_dir}/task-container-build-tools/scripts/usersetup"

case "$(uname -m)" in
  x86_64 | amd64) target_arch=amd64 ;;
  aarch64 | arm64) target_arch=arm64 ;;
  *)
    echo "unsupported host architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

echo ">>> building pinned Infrakube controller image ${CONTROLLER_IMAGE}"
DOCKER_BUILDKIT=1 docker build --platform "linux/${target_arch}" -t "${CONTROLLER_IMAGE}" -f "${source_dir}/build/Dockerfile" "${source_dir}"

echo ">>> building pinned Infrakube task image ${TASK_IMAGE}"
DOCKER_BUILDKIT=1 docker build --platform "linux/${target_arch}" --build-arg "TARGETARCH=${target_arch}" \
  -t "${TASK_IMAGE}" \
  -f "${source_dir}/task-container-build-tools/containerfiles/infrakube-task.Containerfile" \
  "${source_dir}/task-container-build-tools"

echo ">>> loading locally built images into ${KIND_CLUSTER_NAME}"
kind load docker-image --name "${KIND_CLUSTER_NAME}" "${CONTROLLER_IMAGE}"
kind load docker-image --name "${KIND_CLUSTER_NAME}" "${TASK_IMAGE}"

echo ">>> installing manifests from pinned Infrakube source"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/namespace.yaml"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/crds/"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" wait --for=condition=Established \
  crd/terraforms.infrakube.galleybytes.com crd/tofus.infrakube.galleybytes.com --timeout=120s
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/serviceaccount.yaml"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/clusterrole.yaml"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/clusterrolebinding.yaml"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/pvc.yaml"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f "${source_dir}/deploy/service.yaml"

# Render the deployment with the local pinned image before it reaches the API
# server. The upstream manifest names :latest, and this POC must never start or
# pull that mutable image, even briefly.
sed \
  -e "s|image: \"ghcr.io/galleybytes/infrakube:latest\"|image: \"${CONTROLLER_IMAGE}\"|" \
  -e 's|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|' \
  "${source_dir}/deploy/deployment.yaml" | \
  kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" apply -f -

controller_args="$(printf '[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["--zap-log-level=debug","--zap-encoder=console","--auto-download=true","--tf-download-base-url=https://releases.hashicorp.com/terraform","--tofu-download-base-url=https://github.com/opentofu/opentofu/releases/download","--task-image=%s"]}]' "${TASK_IMAGE}")"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" -n infrakube-system patch deployment infrakube --type=json -p "${controller_args}"
kubectl --kubeconfig "${RUNTIME_KUBECONFIG}" -n infrakube-system rollout status deployment/infrakube --timeout=180s

echo ">>> pinned Infrakube controller and task image are ready"
