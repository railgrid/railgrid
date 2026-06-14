#!/usr/bin/env python3
# Copyright 2026 The Faros Authors. Apache-2.0.
#
# Assemble providers/app-studio/manifest.yaml by inlining the
# apigen-generated Project APIResourceSchema under spec.apiExport.schemas[].
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, ".."))
SCHEMA = os.path.join(ROOT, "config", "kcp", "apiresourceschema-projects.ai.kedge.faros.sh.yaml")

HEADER = """\
# App Studio provider - register with the kedge hub.
#
# App Studio gives tenants a persistent AI project workspace: project metadata,
# durable "memory" (goals/requirements/constraints), and a chat surface backed
# by the tenant's own LLM credentials, with optional MCP tool use against the
# workspace.
#
# Apply this against the kcp workspace where the kedge.faros.sh APIExport is
# bound (typically root:kedge:providers when running embedded kcp for
# development):
#
#   kubectl --kubeconfig kcp-admin.kubeconfig apply -f manifest.yaml
#
# For local dev the provider binary runs on the host (PORT=8085, see the
# Tiltfile run-provider-app-studio target) and the hub reverse-proxies both the
# portal bundle and the /api backend to the loopback URLs below. For in-cluster
# deployment, the Helm chart renders the Service DNS instead, e.g.
#   url: http://app-studio.app-studio.svc.cluster.local:8081
#
# GENERATED FILE. The spec.apiExport.schemas[].body block is inlined from
# providers/app-studio/config/kcp/apiresourceschema-projects.ai.kedge.faros.sh.yaml.
# Edit the Go API types and re-run `make codegen-app-studio-provider`.
---
apiVersion: providers.kedge.faros.sh/v1alpha1
kind: CatalogEntry
metadata:
  name: app-studio
  annotations:
    providers.kedge.faros.sh/builtin: "false"
spec:
  displayName: "App Studio"
  description: "Persistent AI project workspace for planning, memory, and chat."
  vendor: "kedge"
  version: "0.1.0"
  category: "AI"
  iconURL: "/ui/providers/app-studio/icon.svg"
  serviceAccountNamespace: "app-studio"
  dependencies:
    - name: code
  ui:
    # Dev loopback port (PORT / Tiltfile run-provider-app-studio). 8085 to
    # avoid colliding with quickstart (8081) and kuery (8084).
    url: "http://localhost:8085"
    indexPath: "/"
  backend:
    # The hub backend proxy forwards /services/providers/app-studio/* here,
    # injecting the verified X-Kedge-Tenant/X-Kedge-User headers and the
    # caller's bearer token. The provider serves /api/projects/* + /healthz.
    url: "http://localhost:8085"
    healthPath: "/healthz"

  apiExport:
    name: "ai.kedge.faros.sh"
    # App Studio stores per-workspace LLM credentials in a Secret in the
    # tenant workspace; the claim lets the provider read/write it as the
    # calling user.
    permissionClaims:
      - resource: secrets
        verbs: ["get", "list", "watch", "create", "update", "delete"]
        tenantScoped: true
    schemas:
      - groupResource: "projects.ai.kedge.faros.sh"
        body: |
"""


def indent_block(text, spaces):
    pad = " " * spaces
    return "".join((pad + line if line.strip() else line) for line in text.splitlines(keepends=True))


def main():
    with open(SCHEMA) as f:
        body = f.read()
    if body.startswith("---\n"):
        body = body[4:]
    manifest = HEADER + indent_block(body, 10)
    if not manifest.endswith("\n"):
        manifest += "\n"
    dest = os.path.join(ROOT, "manifest.yaml")
    with open(dest, "w") as f:
        f.write(manifest)
    print("wrote", dest, "(%d bytes)" % len(manifest))


if __name__ == "__main__":
    main()
