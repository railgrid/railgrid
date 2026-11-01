#!/usr/bin/env bash
# Verify the source contracts that keep Browser pods able to reach the hub in
# both local Tilt modes and keep preview-domain config consistent. The
# infrastructure check executes only the nested Make target with a fake
# provider binary and skipped build prerequisites; it needs no Tilt runtime,
# cluster, or network access.

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
python3 - "$root_dir" <<'PY'
import ast
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path


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


def infrastructure_make_invocation(source: str):
    """Read the nested make command from the real Infrastructure serve_cmd."""
    tree = ast.parse(source)
    resource = next(
        node for node in ast.walk(tree)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Name)
        and node.func.id == "local_resource"
        and node.args
        and isinstance(node.args[0], ast.Constant)
        and node.args[0].value == "infrastructure"
    )
    fields = {keyword.arg: keyword.value for keyword in resource.keywords}
    serve_cmd = fields.get("serve_cmd")
    format_call = next(
        node for node in ast.walk(serve_cmd)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Attribute)
        and node.func.attr == "format"
    )
    template = format_call.func.value
    require(
        isinstance(template, ast.Constant) and isinstance(template.value, str),
        "Infrastructure serve_cmd must keep its shell command in a string template",
    )
    nested_make = next(
        line.strip() for line in template.value.splitlines()
        if line.strip().startswith("make run-provider-infrastructure")
    )
    invocation = shlex.split(nested_make)
    require(
        invocation[:2] == ["make", "run-provider-infrastructure"],
        "Infrastructure serve_cmd must invoke the Make target directly",
    )
    require(
        invocation[2:] == [
            "RAILGRID_APP_BASE_DOMAIN={base_domain}",
            "RAILGRID_APP_PUBLIC_PORT={public_port}",
        ],
        "Infrastructure serve_cmd must pass its resolved domain and port as Make command-line variables",
    )
    return invocation


INFRASTRUCTURE_MAKE = infrastructure_make_invocation(tilt)


def run_infrastructure_make(env_contents, resolved_domain, resolved_port,
                            inherited_env=None, command_line_config=True):
    """Run the real Make recipe against a fixture .env and a capture stub."""
    with tempfile.TemporaryDirectory(prefix="verify-tilt-browser-make-") as directory:
        fixture = Path(directory)
        (fixture / ".env").write_text(env_contents)
        bindir = fixture / "bin"
        bindir.mkdir()
        capture = fixture / "captured.json"
        jwks = fixture / "preview-bridge-jwks.json"
        jwks.write_text("{}\n")
        fake_provider = bindir / "infrastructure-provider"
        fake_provider.write_text(
            "#!/usr/bin/env python3\n"
            "import json\n"
            "import os\n"
            "from pathlib import Path\n"
            "Path(os.environ['TILT_MAKE_CAPTURE']).write_text(json.dumps({\n"
            "    'domain': os.environ.get('RAILGRID_APP_BASE_DOMAIN'),\n"
            "    'port': os.environ.get('RAILGRID_APP_PUBLIC_PORT'),\n"
            "}))\n"
        )
        fake_provider.chmod(0o755)

        if command_line_config:
            nested_args = [
                part.format(base_domain=resolved_domain, public_port=resolved_port)
                for part in INFRASTRUCTURE_MAKE
            ]
        else:
            # Negative control for the old `RAILGRID_APP_*=... make ...` form:
            # those values reached Make as environment variables, so included
            # .env assignments replaced them.
            nested_args = INFRASTRUCTURE_MAKE[:2]

        command = [
            "make",
            "--no-print-directory",
            "-f",
            str(root / "Makefile"),
            # The target recipe below runs, while real provider build/key
            # prerequisites are treated as already current.
            "-o",
            "build-infrastructure-provider",
            "-o",
            "app-studio-preview-bridge-dev-key",
            *nested_args[1:],
            "BINDIR=" + str(bindir),
            # The recipe echoes its token; never inherit a real one into test
            # diagnostics if the stub fails.
            "KROMC_TOKEN=fixture-token",
            "APP_STUDIO_PREVIEW_BRIDGE_DEV_JWKS=" + str(jwks),
        ]
        env = dict(os.environ)
        for name in ("MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS"):
            env.pop(name, None)
        env["TILT_MAKE_CAPTURE"] = str(capture)
        env["RAILGRID_APP_BASE_DOMAIN"] = resolved_domain
        env["RAILGRID_APP_PUBLIC_PORT"] = resolved_port
        if inherited_env:
            env.update(inherited_env)
        result = subprocess.run(
            command, cwd=fixture, env=env, capture_output=True, text=True
        )
        require(
            result.returncode == 0,
            "nested Infrastructure Make recipe failed without reaching the provider stub: "
            + result.stderr[-1200:]
            + result.stdout[-1200:],
        )
        require(capture.is_file(), "nested Infrastructure Make recipe must launch the provider stub")
        return json.loads(capture.read_text())


generated_checkout_domain = "apps-checkout-abc123.127.0.0.1.sslip.io"
blank_domain_result = run_infrastructure_make(
    "RAILGRID_APP_BASE_DOMAIN=\nRAILGRID_APP_PUBLIC_PORT=10443\n",
    generated_checkout_domain,
    "10443",
)
require(
    blank_domain_result == {"domain": generated_checkout_domain, "port": "10443"},
    "a blank root .env domain must not replace Tilt's generated checkout domain",
)

conflicting_env_result = run_infrastructure_make(
    "RAILGRID_APP_BASE_DOMAIN=apps-dotenv.example.test\nRAILGRID_APP_PUBLIC_PORT=7443\n",
    "apps-resolved.example.test",
    "8443",
    inherited_env={
        "RAILGRID_APP_BASE_DOMAIN": "apps-inherited.example.test",
        "RAILGRID_APP_PUBLIC_PORT": "9443",
    },
)
require(
    conflicting_env_result == {"domain": "apps-resolved.example.test", "port": "8443"},
    "resolved Tilt values, including an explicit 8443 port, must beat inherited env and root .env values",
)

empty_port_result = run_infrastructure_make(
    "RAILGRID_APP_BASE_DOMAIN=apps-dotenv.example.test\nRAILGRID_APP_PUBLIC_PORT=7443\n",
    "apps-resolved.example.test",
    "",
)
require(
    empty_port_result == {"domain": "apps-resolved.example.test", "port": ""},
    "an explicitly empty public port from Tilt must survive a conflicting root .env port",
)

old_inline_env_result = run_infrastructure_make(
    "RAILGRID_APP_BASE_DOMAIN=apps-dotenv.example.test\nRAILGRID_APP_PUBLIC_PORT=7443\n",
    "apps-resolved.example.test",
    "8443",
    command_line_config=False,
)
require(
    old_inline_env_result == {"domain": "apps-dotenv.example.test", "port": "7443"},
    "the negative control must reproduce how the former inline-environment form lets .env win",
)
require(
    old_inline_env_result != conflicting_env_result,
    "the nested-Make regression check must distinguish command-line values from the former inline-env form",
)


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


def tilt_resource_field(source: str, resource_name: str, field_name: str):
    tree = ast.parse(source)
    resource = next(
        node for node in ast.walk(tree)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Name)
        and node.func.id == "local_resource"
        and node.args
        and isinstance(node.args[0], ast.Constant)
        and node.args[0].value == resource_name
    )
    fields = {keyword.arg: keyword.value for keyword in resource.keywords}
    return fields[field_name]


def evaluate_tilt_string_expression(node, bindings):
    """Evaluate the literal string formatting used by the gateway resource."""
    if isinstance(node, ast.Constant):
        return node.value
    if isinstance(node, ast.Name):
        return bindings[node.id]
    if isinstance(node, ast.Tuple):
        return tuple(evaluate_tilt_string_expression(item, bindings) for item in node.elts)
    if isinstance(node, ast.BinOp):
        left = evaluate_tilt_string_expression(node.left, bindings)
        right = evaluate_tilt_string_expression(node.right, bindings)
        if isinstance(node.op, ast.Add):
            return left + right
        if isinstance(node.op, ast.Mod):
            return left % right
    raise SystemExit("preview-gateway-up command must remain a string-format expression")


gateway_bindings = {
    "preview_kro_context": "kind-railgrid-kro-test",
    "preview_app_base_domain": "apps-resolved.example.test",
    "preview_gateway_port": "10443",
    "preview_gateway_service_port": "443",
}
gateway_command = evaluate_tilt_string_expression(
    tilt_resource_field(tilt, "preview-gateway-up", "cmd"),
    gateway_bindings,
)
gateway_invocation = shlex.split(gateway_command)
require(
    gateway_invocation[:2] == ["make", "dev-preview-gateway-up"]
    and gateway_invocation[2:] == [
        "PREVIEW_GATEWAY_CONTEXT=kind-railgrid-kro-test",
        "PREVIEW_GATEWAY_HOSTNAME=*.apps-resolved.example.test",
        "PREVIEW_GATEWAY_PORT=10443",
        "PREVIEW_GATEWAY_SERVICE_PORT=443",
    ],
    "Tilt preview-gateway-up must pass its resolved configuration after the Make target",
)


def make_gateway_target_values(command_line_config: bool):
    """Dry-run the actual Tilt target command against a conflicting .env."""
    with tempfile.TemporaryDirectory(prefix="verify-tilt-gateway-make-") as directory:
        fixture = Path(directory)
        env_file = fixture / ".env"
        env_file.write_text(
            "PREVIEW_GATEWAY_CONTEXT=dotenv-context\n"
            "PREVIEW_GATEWAY_HOSTNAME=*.apps-dotenv.example.test\n"
            "PREVIEW_GATEWAY_PORT=9443\n"
            "PREVIEW_GATEWAY_SERVICE_PORT=7443\n"
        )
        command = [
            "make",
            "--no-print-directory",
            "-n",
            "-f",
            str(root / "Makefile"),
            *gateway_invocation[1:2],
        ]
        env = dict(os.environ)
        for name in (
            "MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "ENV_FILES",
            "PREVIEW_GATEWAY_CONTEXT", "PREVIEW_GATEWAY_HOSTNAME",
            "PREVIEW_GATEWAY_PORT", "PREVIEW_GATEWAY_SERVICE_PORT",
        ):
            env.pop(name, None)
        if command_line_config:
            command.extend(gateway_invocation[2:])
        else:
            # Negative control for the old prefix form:
            # PREVIEW_GATEWAY_* assignments before `make` arrive as environment
            # values, which the included .env is allowed to replace.
            for assignment in gateway_invocation[2:]:
                name, value = assignment.split("=", 1)
                env[name] = value
        command.append("ENV_FILES=" + str(env_file))
        result = subprocess.run(
            command, cwd=fixture, env=env, capture_output=True, text=True
        )
        require(
            result.returncode == 0,
            "preview Gateway Make dry-run failed: " + result.stderr[-1200:] + result.stdout[-1200:],
        )
        values = {}
        for name in (
            "PREVIEW_GATEWAY_CONTEXT",
            "PREVIEW_GATEWAY_HOSTNAME",
            "PREVIEW_GATEWAY_PORT",
            "PREVIEW_GATEWAY_SERVICE_PORT",
        ):
            match = re.search(r'(?m)^\s*' + name + r'="([^"]*)"', result.stdout)
            require(match is not None, "Make dry-run must expose " + name)
            values[name] = match.group(1)
        return values


tilt_gateway_values = make_gateway_target_values(command_line_config=True)
require(
    tilt_gateway_values == {
        "PREVIEW_GATEWAY_CONTEXT": "kind-railgrid-kro-test",
        "PREVIEW_GATEWAY_HOSTNAME": "*.apps-resolved.example.test",
        "PREVIEW_GATEWAY_PORT": "10443",
        "PREVIEW_GATEWAY_SERVICE_PORT": "443",
    },
    "Make must retain the resolved Tilt Gateway values, including the empty-port HTTPS 443 semantic",
)
old_gateway_values = make_gateway_target_values(command_line_config=False)
require(
    old_gateway_values == {
        "PREVIEW_GATEWAY_CONTEXT": "dotenv-context",
        "PREVIEW_GATEWAY_HOSTNAME": "*.apps-dotenv.example.test",
        "PREVIEW_GATEWAY_PORT": "9443",
        "PREVIEW_GATEWAY_SERVICE_PORT": "7443",
    }
    and old_gateway_values != tilt_gateway_values,
    "the negative control must show .env replacing the former inline-environment Gateway values",
)


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
    "# `tilt-checkout.py prepare-env` resolves the app domain once.",
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
    and "make dev-preview-gateway-up" in regular_gateway
    and "preview_app_base_domain" in regular_gateway
    and "'.env'" in regular_gateway
    and "'Tiltfile'" in regular_gateway,
    "regular Tilt preview Gateway must reconcile the shared wildcard when the domain config changes",
)
require(
    "RAILGRID_APP_BASE_DOMAIN='{base_domain}'" in regular_infrastructure
    and "RAILGRID_APP_PUBLIC_PORT='{public_port}'" in regular_infrastructure
    and "make run-provider-infrastructure" in regular_infrastructure
    and "RAILGRID_APP_BASE_DOMAIN={base_domain} \\" not in regular_infrastructure
    and "RAILGRID_APP_PUBLIC_PORT={public_port} \\" not in regular_infrastructure
    and "base_domain=preview_app_base_domain" in regular_infrastructure
    and "public_port=preview_app_public_port" in regular_infrastructure
    and "'.env'" in regular_infrastructure,
    "regular Tilt Infrastructure provider must pass and watch the resolved domain and port as Make command-line variables",
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
