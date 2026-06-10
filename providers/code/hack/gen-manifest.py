#!/usr/bin/env python3
# Copyright 2026 The Faros Authors. Apache-2.0.
#
# Assembles providers/code/manifest.yaml (the CatalogEntry the hub provisions
# from) by inlining the four apigen-generated APIResourceSchema bodies under
# spec.apiExport.schemas[].body. Re-run after `make codegen-code-provider`:
#
#     python3 providers/code/hack/gen-manifest.py
#
# Run from the repo root or anywhere — paths are resolved relative to this file.
import os
import textwrap

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, ".."))
KCP = os.path.join(ROOT, "config", "kcp")

# resource -> APIResourceSchema file. Order is cosmetic.
SCHEMAS = [
    ("connections.code.kedge.faros.sh", "apiresourceschema-connections.code.kedge.faros.sh.yaml"),
    ("repositories.code.kedge.faros.sh", "apiresourceschema-repositories.code.kedge.faros.sh.yaml"),
    ("deploykeys.code.kedge.faros.sh", "apiresourceschema-deploykeys.code.kedge.faros.sh.yaml"),
    ("collaborators.code.kedge.faros.sh", "apiresourceschema-collaborators.code.kedge.faros.sh.yaml"),
]

HEADER = """\
# code provider — register with the kedge hub.
#
# Apply to the kcp workspace where the kedge.faros.sh APIExport is bound
# (typically root:kedge:providers when running embedded kcp for development):
#
#   kubectl --kubeconfig kcp-admin.kubeconfig apply -f manifest.yaml
#
# GENERATED FILE. The spec.apiExport.schemas[].body blocks are inlined from
# providers/code/config/kcp/apiresourceschema-*.yaml. Edit the Go API types and
# re-run `make codegen-code-provider && python3 providers/code/hack/gen-manifest.py`.
#
# Unlike the infrastructure provider (schemas: []), the code provider ships its
# four tenant-authored CRDs as static schemas so tenants who APIBind get
# Connection / Repository / DeployKey / Collaborator authorable in their own
# workspace. The hub provisioner (pkg/hub/providers/provision.go) applies these
# with storage: {crd: {}} and references them from the APIExport.
---
apiVersion: providers.kedge.faros.sh/v1alpha1
kind: CatalogEntry
metadata:
  name: code
spec:
  displayName: "Code"
  description: "Manage source-code repositories and access across git providers (GitHub)."
  vendor: "kedge"
  version: "0.1.0"
  category: "Developer"
  iconURL: "/ui/providers/code/icon.svg"
  serviceAccountNamespace: "code"
  # Local dev (Tilt / Makefile): the provider binary runs on the host at
  # PORT=8083 — distinct from quickstart (:8081) and infrastructure (:8082).
  # For in-cluster deployment, change to the Service DNS, e.g.
  # http://code.code.svc.cluster.local:8083.
  ui:
    url: "http://localhost:8083"
    indexPath: "/"
    children:
      - displayName: Connections
        builtinRoute: connections
      - displayName: Repositories
        builtinRoute: repositories
  backend:
    url: "http://localhost:8083"
    healthPath: "/healthz"
  apiExport:
    name: "code.providers.kedge.faros.sh"
    # The controllers read each Connection's PAT Secret AND write the generated
    # DeployKey private-key Secret in the tenant workspace, so the secrets claim
    # needs write verbs (infra only needed read). tenantScoped: true auto-accepts
    # on Enable.
    permissionClaims:
      - resource: secrets
        verbs: [get, list, watch, create, update, patch, delete]
        tenantScoped: true
    schemas:
"""


def indent_block(text, spaces):
    pad = " " * spaces
    # Keep trailing newline handling clean; do not indent blank lines' trailing
    # whitespace beyond the pad (YAML block scalars tolerate it, but be tidy).
    return "".join((pad + line if line.strip() else line) for line in text.splitlines(keepends=True))


def main():
    out = [HEADER]
    for group_resource, fname in SCHEMAS:
        body = open(os.path.join(KCP, fname)).read()
        # Strip a leading document separator if apigen emitted one.
        if body.startswith("---\n"):
            body = body[4:]
        out.append("      - groupResource: %s\n" % group_resource)
        out.append("        body: |\n")
        out.append(indent_block(body, 10))
        if not out[-1].endswith("\n"):
            out.append("\n")
    manifest = "".join(out)
    dest = os.path.join(ROOT, "manifest.yaml")
    with open(dest, "w") as f:
        f.write(manifest)
    print("wrote", dest, "(%d bytes)" % len(manifest))


if __name__ == "__main__":
    main()
