# The image every Prow job in .prow.yaml runs in: the toolchains the GitHub
# Actions jobs get from setup-go / setup-node / setup-python / setup-helm /
# kind-action, plus Docker for the kind suites and PostgreSQL for the App
# Studio store tests. Published as ghcr.io/railgrid/railgrid-ci:<VERSION> by
# .github/workflows/ci-image.yaml. See hack/ci/prow/README.md before changing it.
#
# ubuntu:24.04 on purpose: it is the runner release the kind suites are pinned
# to in e2e.yaml, and it ships Python 3.12 and PostgreSQL 16.
FROM ubuntu:24.04

ARG TARGETARCH
# Keep GO_VERSION on the root go.mod's `go` line. A module that asks for a
# newer toolchain still works (GOTOOLCHAIN=auto downloads it), only slower.
ARG GO_VERSION=1.26.10
ARG NODE_VERSION=22.23.5
ARG HELM_VERSION=v4.3.1
ARG KUBECTL_VERSION=v1.37.1
ARG KIND_VERSION=v0.33.0
ARG PYYAML_VERSION=6.0.3

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git make gcc g++ libc6-dev jq unzip xz-utils file \
      openssl openssh-client rsync sudo procps iproute2 iptables kmod tini \
      python3 python3-pip python3-venv \
      postgresql-16 \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=${TARGETARCH} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -xz -C /usr/local

RUN case "${TARGETARCH}" in amd64) node_arch=x64 ;; *) node_arch="${TARGETARCH}" ;; esac \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz" \
       | tar -xJ -C /usr/local --strip-components=1 --exclude='*.md' --exclude=LICENSE

RUN curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
       | tar -xz -C /usr/local/bin --strip-components=1 "linux-${TARGETARCH}/helm" \
    && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && curl -fsSL -o /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${TARGETARCH}" \
    && chmod 0755 /usr/local/bin/kubectl /usr/local/bin/kind

# hack/ci/requirements-test.txt, which `make verify-ci-selection` asserts.
RUN pip3 install --no-cache-dir --break-system-packages "PyYAML==${PYYAML_VERSION}"

# Jobs run as an unprivileged user with passwordless sudo, like a GitHub
# runner: several Go tests assert on permission errors that root never gets.
# runner.sh starts as root (Prow's default), prepares the pod, then drops to it.
RUN useradd --create-home --shell /bin/bash --groups docker,sudo ci \
    && echo 'ci ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ci \
    && git config --system --add safe.directory '*'

ENV PATH=/usr/local/go/bin:/home/ci/go/bin:/usr/lib/postgresql/16/bin:${PATH}

COPY runner.sh /usr/local/bin/runner.sh
RUN chmod 0755 /usr/local/bin/runner.sh
