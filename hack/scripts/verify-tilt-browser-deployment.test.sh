#!/usr/bin/env bash
# Verify the source contracts that keep Browser pods able to reach the hub in
# both local Tilt modes and keep preview-domain config consistent. This is
# intentionally static: evaluating either Tiltfile would require a live
# cluster and would not make the wiring itself regression-testable.

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
python3 - "$root_dir" <<'PY'
from pathlib import Path
import os
import re
import subprocess
import sys


root = Path(sys.argv[1])
tilt = (root / "Tiltfile").read_text()
cluster = (root / "Tiltfile.cluster").read_text()
browser = (root / "providers/infrastructure/install/templates/browser.yaml").read_text()
makefile = (root / "Makefile").read_text()


def section(source: str, start: str, end: str) -> str:
    start_at = source.index(start)
    end_at = source.index(end, start_at + len(start))
    return source[start_at:end_at]


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def make_gateway_hostname(domain: str) -> str:
    env = dict(os.environ)
    env.pop("RAILGRID_APP_BASE_DOMAIN", None)
    command = ["make", "-n", "dev-preview-gateway-up", "ENV_FILES="]
    command.append("RAILGRID_APP_BASE_DOMAIN=" + domain)
    result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True, check=True)
    match = re.search(r'PREVIEW_GATEWAY_HOSTNAME="([^"]+)"', result.stdout)
    require(match is not None, "Makefile dry run must expose the preview Gateway hostname")
    return match.group(1)


def make_gateway_service_port(public_port):
    env = dict(os.environ)
    env.pop("RAILGRID_APP_PUBLIC_PORT", None)
    command = ["make", "-n", "dev-preview-gateway-up", "ENV_FILES=", "RAILGRID_APP_BASE_DOMAIN="]
    if public_port is not None:
        command.append("RAILGRID_APP_PUBLIC_PORT=" + public_port)
    result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True, check=True)
    match = re.search(r'PREVIEW_GATEWAY_SERVICE_PORT="([^"]*)"', result.stdout)
    require(match is not None, "Makefile dry run must expose the preview Gateway Service port")
    return match.group(1)


require(
    make_gateway_hostname("apps.example.test") == "*.apps.example.test",
    "manual preview Gateway reconcile must derive its wildcard from RAILGRID_APP_BASE_DOMAIN",
)
require(
    make_gateway_hostname("") == "*.apps.127.0.0.1.sslip.io",
    "manual preview Gateway reconcile must retain the local sslip.io fallback",
)
require(
    make_gateway_service_port(None) == "10443"
    and make_gateway_service_port("") == "443"
    and make_gateway_service_port("8443") == "8443",
    "manual preview Gateway reconcile must preserve the local port default and explicit public-port semantics",
)


digests = re.findall(
    r"(?m)^\s*image:\s*mcr\.microsoft\.com/playwright/mcp@sha256:([0-9a-f]+)\s*$",
    browser,
)
require(
    len(digests) == 1,
    "browser template deployment must contain exactly one digest-pinned Playwright MCP image field",
)
require(
    re.fullmatch(r"[0-9a-f]{64}", digests[0]) is not None,
    f"Playwright MCP digest must be exactly 64 lowercase hex characters: {digests[0]!r}",
)
require(re.search(r"(?m)^\s*- --isolated\s*$", browser) is not None,
        "browser template must explicitly isolate MCP session contexts")
require(re.search(r"(?m)^\s*- --shared-browser-context\s*$", browser) is None,
        "browser template must not share page/cookie state across MCP clients")

regular_app = section(
    tilt,
    "local_resource(\n    'app-studio',",
    "\n\nlocal_resource(\n    'app-studio-db-down',",
)
require(
    "APP_STUDIO_HUB_PUBLIC_URL=%s" in regular_app
    and "preview_hub_public_url" in regular_app,
    "regular Tilt App Studio must receive preview_hub_public_url as its public hub URL",
)

preview_config = section(
    tilt,
    "# `tilt-checkout.py prepare-env` resolves the app domain once:",
    "# Observe disk headroom without deleting data or caches.",
)
require(
    "watch_file('.env')" in tilt
    and "watch_file('hack/scripts/tilt-checkout.py')" in tilt,
    "regular Tilt must reload resolved config when root .env or its resolver changes",
)
require(
    "preview_app_base_domain = os.getenv('RAILGRID_APP_BASE_DOMAIN', '')" in preview_config
    and "if not preview_app_base_domain:" in preview_config,
    "regular Tilt must use the checkout helper's resolved app domain without a second fallback",
)
require(
    "preview_app_public_port = os.getenv('RAILGRID_APP_PUBLIC_PORT', preview_gateway_port)" in preview_config,
    "regular Tilt must preserve an explicitly empty public app port from the shared config",
)
require(
    "preview_gateway_service_port = preview_app_public_port or '443'" in preview_config,
    "regular Tilt must expose the Envoy Service on standard HTTPS when the public port is empty",
)

regular_dns = section(
    tilt,
    "local_resource(\n    'app-studio-preview-dns',",
    "\n\nlocal_resource(\n    'kro-mgmt-down',",
)
regular_gateway = section(
    tilt,
    "local_resource(\n    'preview-gateway-up',",
    "\n\n# With the default sslip.io domain",
)
regular_preview_tunnel = section(
    tilt,
    "local_resource(\n    'app-studio-preview-port-forward',",
    "\n\n# ---------------------------------------------------------------------------\n# edges —",
)
regular_infrastructure = section(
    tilt,
    "local_resource(\n    'infrastructure',",
    "\n\nlocal_resource(\n    'infrastructure-register',",
)
require(
    'PREVIEW_GATEWAY_HOSTNAME="*.%s"' in regular_gateway
    and "preview_app_base_domain" in regular_gateway
    and "'.env'" in regular_gateway
    and "'Tiltfile'" in regular_gateway,
    "regular Tilt preview Gateway must reconcile the shared wildcard when the domain config changes",
)
require(
    "RAILGRID_APP_BASE_DOMAIN={base_domain}" in regular_infrastructure
    and "base_domain=preview_app_base_domain" in regular_infrastructure
    and "'.env'" in regular_infrastructure,
    "regular Tilt Infrastructure provider must receive and watch the same resolved app domain",
)
require(
    "preview_app_base_domain" in regular_dns and "'.env'" in regular_dns,
    "regular Tilt browser DNS must watch and use the shared app domain",
)
require(
    "preview_app_base_domain" in regular_preview_tunnel
    and "deps=['Tiltfile', '.env']" in regular_preview_tunnel
    and "port-forward \"svc/$svc\" %s:%s" in regular_preview_tunnel,
    "regular Tilt preview tunnel probe must restart with the shared app domain",
)
access_proxy_image = section(
    tilt,
    "local_resource(\n    'access-proxy-image',",
    "\n\n# --- Dev agent image",
)
access_proxy_deps_match = re.search(
    r"(?ms)^access_proxy_image_deps\s*=\s*\[(.*?)^\]",
    tilt,
)
require(
    "preview_hub_public_host" in regular_dns,
    "regular Tilt preview DNS must map the public hub hostname for Browser pods",
)
require(
    "getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR == 1 {print $1}'" in regular_dns,
    "regular Tilt preview DNS must select exactly one IPv4 host gateway address",
)
require(
    "getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR == 1 {{print $1}}'"
    in regular_infrastructure,
    "regular Tilt infrastructure must select exactly one IPv4 host gateway address",
)
require(
    "make load-access-proxy-image VERSION=dev KRO_KIND_NAME={kind_name}" in access_proxy_image,
    "regular Tilt must build and load the dev access-proxy image into its preview kind cluster",
)
require(
    "load-access-proxy-image: docker-build-access-proxy" in makefile
    and "kind load docker-image $(ACCESS_PROXY_IMAGE) --name $(KRO_KIND_NAME)" in makefile,
    "Makefile must build and locally load the requested access-proxy tag",
)
require(
    "resource_deps=['kro-mgmt-up']" in access_proxy_image,
    "regular Tilt access-proxy image loading must wait for the preview kind cluster",
)
require(
    "kubectl --kubeconfig={kro_kubeconfig} config current-context" in access_proxy_image
    and "kubectl --kubeconfig={kro_kubeconfig} get nodes -o name" in access_proxy_image,
    "regular Tilt must validate the configured preview kubeconfig context and node before loading the image",
)
require(
    "access_proxy_repository}@$access_proxy_digest" in access_proxy_image,
    "regular Tilt must create a digest-qualified access-proxy image alias in containerd",
)
require(
    access_proxy_deps_match is not None
    and all(
        path in access_proxy_deps_match.group(1)
        for path in [
            "providers/infrastructure/Dockerfile.access-proxy",
            "providers/infrastructure/accessproxy",
            "providers/infrastructure/cmd/access-proxy",
            "provider-sdk",
        ]
    )
    and "deps=access_proxy_image_deps" in access_proxy_image
    and "] + access_proxy_image_deps," in regular_infrastructure,
    "regular Tilt must watch shared access-proxy image inputs both to rebuild the image and restart Infrastructure so it re-renders the pinned image",
)
require(
    "'access-proxy-image'" in tilt
    and "resource_deps=infrastructure_resource_deps" in regular_infrastructure
    and "{access_proxy_env}" in regular_infrastructure
    and "infrastructure_access_proxy_env = 'RAILGRID_ACCESS_PROXY_IMAGE=\"{repository}@$access_proxy_digest\"" in tilt,
    "regular Tilt infrastructure must depend on and pass its immutable access-proxy image",
)

cluster_dns_setup = section(
    cluster,
    "app_studio_preview_dns_apply =",
    "\nhub_host_aliases =",
)
for needle, message in [
    ("hub_browser_host", "cluster Tilt DNS must use the hub hostname derived from hub_external_url"),
    ("HUB_SERVICE=%s", "cluster Tilt DNS must identify the hub Service"),
    ('get svc "$HUB_SERVICE"', "cluster Tilt DNS must resolve the hub Service IP"),
    ("hub_ip", "cluster Tilt DNS must pass the resolved hub Service IP"),
    ("configure-tilt-preview-dns.sh", "cluster Tilt DNS must use the managed CoreDNS helper"),
]:
    require(needle in cluster_dns_setup, message)

cluster_dns_resource = section(
    cluster,
    "local_resource(\n    'app-studio-preview-dns',",
    "\n\n# ---------------------------------------------------------------------------\n# Static-token auth",
)
require(
    "resource_deps=['railgrid-hub']" in cluster_dns_resource,
    "cluster Tilt preview DNS must wait for the railgrid-hub Service",
)

cluster_app = section(
    cluster,
    "provider_pod(\n    'app-studio',",
    "\n\nlocal_resource(\n    'app-studio-register',",
)
require(
    "'hub.publicURL=%s' % hub_external_url" in cluster_app,
    "cluster Tilt App Studio must retain the browser-reachable hub_external_url",
)

print("verified browser digest shape and regular/cluster Tilt hub reachability wiring")
PY
