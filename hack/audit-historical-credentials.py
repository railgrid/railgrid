#!/usr/bin/env python3
"""Read-only, bounded credential/RBAC inventory for one legacy team or org-provider workspace."""

import argparse
import http.client
import json
import re
import select
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlencode, urlsplit, urlunsplit

PAGE_SIZE, MAX_OBJECTS, MAX_SECONDS = 100, 5000, 60
MAX_RESPONSE, MAX_TOTAL_RESPONSE, MAX_REPORT, REQUEST_TIMEOUT = 4 << 20, 64 << 20, 8 << 20, 8
MAX_ANALYSIS_PRINCIPALS, MAX_ANALYSIS_RULES, MAX_ANALYSIS_EDGES = 5000, 20000, 20000
RETIRED = "railgrid-du-"
PARTIAL = "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1"
TABLE = "application/json;as=Table;g=meta.k8s.io;v=v1"
REQUIRED = {
    ("", "v1", n) for n in ("namespaces", "serviceaccounts", "secrets")
} | {("rbac.authorization.k8s.io", "v1", n) for n in
     ("roles", "rolebindings", "clusterroles", "clusterrolebindings")} | {
    ("core.kcp.io", "v1alpha1", "logicalclusters")}
OPTIONAL = {
    ("apis.kcp.io", "v1alpha2", "apibindings"),
    ("apis.kcp.io", "v1alpha1", "apibindings"),
    ("apis.kcp.io", "v1alpha2", "apiexports"),
    ("apis.kcp.io", "v1alpha1", "apiexports"),
    ("apis.kcp.io", "v1alpha1", "apiresourceschemas"),
    ("apis.kcp.io", "v1alpha1", "apiexportendpointslices"),
    ("apiextensions.k8s.io", "v1", "customresourcedefinitions"),
    *(('admissionregistration.k8s.io', 'v1', n) for n in (
        "mutatingwebhookconfigurations", "validatingwebhookconfigurations",
        "validatingadmissionpolicies", "validatingadmissionpolicybindings")),
}
CONTROL_GROUPS = {
    "*", "k8s.io", "kcp.io", "core.kcp.io", "apis.kcp.io", "tenancy.kcp.io",
    "rbac.authorization.k8s.io", "authentication.k8s.io", "authorization.k8s.io",
    "admissionregistration.k8s.io", "apiextensions.k8s.io", "apiregistration.k8s.io",
    "certificates.k8s.io",
}
CONTROL_VERBS = {"*", "impersonate", "bind", "escalate", "approve", "sign", "use"}
SAFE_LABELS = {"railgrid.ai/delegated-user", "railgrid.ai/workload-identity",
               "tenants.railgrid.ai/railgrid-sa", "tenancy.railgrid.ai/managed-by"}


class AuditError(Exception):
    pass


def workspace_arg(value):
    parts = value.split(":")
    team = len(parts) == 5 and parts[:3] == ["root", "railgrid", "tenants"] and parts[4] != "providers" and all(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", parts[i]) for i in (3, 4))
    provider = len(parts) == 6 and parts[:3] == ["root", "railgrid", "tenants"] and parts[4] == "providers" and all(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", parts[i]) for i in (3, 5))
    if not (team or provider):
        raise argparse.ArgumentTypeError("workspace must be one team or org-provider path under root:railgrid:tenants:<org>")
    return value


def target_server(server, workspace):
    u = urlsplit(server)
    if (u.scheme not in ("http", "https") or not u.netloc or u.username or u.password or u.query or u.fragment
            or (u.path.rstrip("/") and not re.fullmatch(r"/clusters/[^/]+", u.path.rstrip("/")))):
        raise AuditError("kubeconfig API server must be HTTP(S) with an empty path or /clusters/<id>")
    return urlunsplit((u.scheme, u.netloc, "/clusters/" + workspace, "", ""))


def selected_server(kubeconfig):
    try:
        p = subprocess.run(["kubectl", "--kubeconfig", str(kubeconfig), "config", "view", "--minify", "-o", "json"],
                           check=True, capture_output=True, text=True, timeout=10)
        return json.loads(p.stdout)["clusters"][0]["cluster"]["server"]
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, IndexError, TypeError) as exc:
        raise AuditError("cannot read selected kubeconfig API server") from exc


class Proxy:
    def __init__(self, kubeconfig, server):
        self.cmd = ["kubectl", "--kubeconfig", str(kubeconfig), "--server", server, "proxy",
                    "--address=127.0.0.1", "--port=0", "--append-server-path=true", "--accept-paths=^/(api|apis)(/|$)",
                    "--reject-methods=^(|[^G].*|G($|[^E].*)|GE($|[^T].*)|GET.+)$"]
        self.proc = None
        self.port = None
        self.last_bytes = 0

    def __enter__(self):
        try:
            self.proc = subprocess.Popen(self.cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if self.proc.poll() is not None:
                    raise AuditError("kubectl proxy exited before ready")
                ready, _, _ = select.select([self.proc.stdout], [], [], 0.2)
                if ready:
                    m = re.search(r"127\.0\.0\.1:(\d+)", self.proc.stdout.readline()[:2048])
                    if m:
                        self.port = int(m.group(1))
                        return self
            raise AuditError("timed out starting kubectl proxy")
        except Exception:
            self.close()
            raise

    def close(self):
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait(timeout=3)

    def __exit__(self, *_):
        self.close()

    def get(self, path, accept, timeout):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=timeout)
        try:
            conn.request("GET", path, headers={"Accept": accept, "User-Agent": "railgrid-credential-audit/1"})
            r = conn.getresponse()
            body = r.read(MAX_RESPONSE + 1)
            if len(body) > MAX_RESPONSE:
                raise AuditError("API response byte limit exceeded")
            self.last_bytes = len(body)
            if not 200 <= r.status < 300:
                raise AuditError(f"HTTP {r.status} for {path.split('?', 1)[0]}")
            value = json.loads(body)
            if not isinstance(value, dict):
                raise AuditError("API returned non-object JSON")
            return value
        except (OSError, http.client.HTTPException, TimeoutError, ValueError) as exc:
            if isinstance(exc, AuditError):
                raise
            raise AuditError("API request failed, timed out, or returned invalid JSON") from exc
        finally:
            conn.close()


def metadata(item):
    m = item.get("metadata") or {}
    labels, annos = m.get("labels") or {}, m.get("annotations") or {}
    marker_labels = {k: True for k in SAFE_LABELS if k in labels}
    safe_annos = {k: v for k, v in annos.items() if k in {
        "railgrid.ai/delegated-user", "railgrid.ai/delegated-org", "railgrid.ai/delegated-workspace",
        "railgrid.ai/delegated-provider", "railgrid.ai/delegated-provider-org",
        "railgrid.ai/workload-identity-tenant", "railgrid.ai/delegated-user-proof-version",
        "tenancy.railgrid.ai/rbac-identity"}}
    return {"name": m.get("name", ""), "namespace": m.get("namespace", ""), "uid": m.get("uid", ""),
            "labels": marker_labels, "annotations": safe_annos}


def path_for(group, version, resource, namespace=None):
    path = f"/apis/{group}/{version}" if group else f"/api/{version}"
    if namespace is not None:
        path += "/namespaces/" + namespace
    return path + "/" + resource


class Scan:
    def __init__(self, proxy, workspace, page, maximum, seconds):
        self.proxy, self.workspace, self.page, self.maximum, self.seconds = proxy, workspace, page, maximum, seconds
        self.started, self.read, self.total_bytes, self.errors, self.discovery = time.monotonic(), 0, 0, [], {}

    def get(self, path, accept="application/json"):
        remain = self.seconds - (time.monotonic() - self.started)
        if remain <= 0:
            raise AuditError("total time limit reached")
        value = self.proxy.get(path, accept, min(REQUEST_TIMEOUT, remain))
        self.total_bytes += self.proxy.last_bytes
        if self.total_bytes > MAX_TOTAL_RESPONSE:
            raise AuditError("total API response byte limit reached")
        return value

    def pages(self, path, accept="application/json"):
        out, token = [], ""
        while True:
            left = self.maximum - self.read
            if left <= 0:
                raise AuditError("total object limit reached")
            query = {"limit": str(min(self.page, left))}
            if token:
                query["continue"] = token
            if accept == TABLE:
                query["includeObject"] = "None"
            page = self.get(path + "?" + urlencode(query), accept)
            items = page.get("rows") if accept == TABLE else page.get("items")
            if not isinstance(items, list) or len(items) > left:
                raise AuditError("invalid or over-limit list response")
            if accept == TABLE and (page.get("kind") != "Table" or any(row.get("object") is not None for row in items)):
                raise AuditError("Secret Table response contained or may contain object payload")
            out.extend(items)
            self.read += len(items)
            token = (page.get("metadata") or {}).get("continue", "")
            if not token:
                return out, page.get("columnDefinitions", [])
            if self.read >= self.maximum:
                raise AuditError("object limit reached before list completed")

    def discover(self):
        self.discovery[("", "v1")] = self.get("/api/v1")
        groups = self.get("/apis").get("groups", [])
        wanted = {g for g, _, _ in REQUIRED | OPTIONAL} | {"authentication.k8s.io"}
        for group in groups:
            name = group.get("name")
            if name not in wanted:
                continue
            for version in group.get("versions", []):
                v = version.get("version", "")
                if v:
                    try:
                        self.discovery[(name, v)] = self.get(f"/apis/{name}/{v}")
                    except AuditError as exc:
                        self.errors.append(f"discovery {name}/{v}: {exc}")

    def resources(self):
        return {(g, v, r.get("name")) for (g, v), doc in self.discovery.items()
                for r in doc.get("resources", []) if "list" in r.get("verbs", [])}

    def list(self, out, g, v, resource, accept="application/json", namespace=None):
        try:
            items, cols = self.pages(path_for(g, v, resource, namespace), accept)
            return {"available": True, "items": items, "columns": cols}
        except AuditError as exc:
            self.errors.append(f"{out}: {exc}")
            return {"available": True, "incomplete": True, "items": []}

    def build(self):
        self.discover()
        available = self.resources()
        missing = REQUIRED - available
        self.errors.extend(f"required API unavailable: {g or 'core'}/{v}/{r}" for g, v, r in missing)
        try:
            logical_cluster = self.get("/apis/core.kcp.io/v1alpha1/logicalclusters/cluster")
            actual_path = (logical_cluster.get("metadata", {}).get("annotations") or {}).get("kcp.io/path", "")
            if actual_path != self.workspace:
                return {"schemaVersion": 1, "scope": {"workspace": self.workspace, "logicalClusterPath": actual_path},
                        "complete": False, "errors": ["LogicalCluster kcp.io/path does not match the requested workspace"],
                        "inventories": {}, "analysis": {}, "counts": {"objectsRead": self.read}}
        except AuditError as exc:
            actual_path = ""
            return {"schemaVersion": 1, "scope": {"workspace": self.workspace}, "complete": False,
                    "errors": [f"LogicalCluster scope verification: {exc}"], "inventories": {},
                    "analysis": {}, "counts": {"objectsRead": self.read}}
        inv = {}
        for key, g, v, r, accept in [
            ("namespaces", "", "v1", "namespaces", PARTIAL),
            ("serviceAccounts", "", "v1", "serviceaccounts", PARTIAL),
            ("roles", "rbac.authorization.k8s.io", "v1", "roles", "application/json"),
            ("roleBindings", "rbac.authorization.k8s.io", "v1", "rolebindings", "application/json"),
            ("clusterRoles", "rbac.authorization.k8s.io", "v1", "clusterroles", "application/json"),
            ("clusterRoleBindings", "rbac.authorization.k8s.io", "v1", "clusterrolebindings", "application/json"),
            ("apiResourceSchemas", "apis.kcp.io", "v1alpha1", "apiresourceschemas", "application/json"),
            ("apiExportEndpointSlices", "apis.kcp.io", "v1alpha1", "apiexportendpointslices", "application/json"),
            ("customResourceDefinitions", "apiextensions.k8s.io", "v1", "customresourcedefinitions", "application/json"),
        ]:
            if (g, v, r) in available:
                inv[key] = self.list(key, g, v, r, accept)
            else:
                inv[key] = {"available": False, "items": []}
        for key, resource in (("apiExports", "apiexports"), ("apiBindings", "apibindings")):
            version = preferred_api_version(available, resource)
            if version:
                inv[key] = self.list(key, "apis.kcp.io", version, resource)
                inv[key]["version"] = version
            else:
                inv[key] = {"available": False, "items": [], "version": None}
                self.errors.append(f"required inventory API unavailable: apis.kcp.io/{resource}")
        ns = [metadata(x)["name"] for x in inv["namespaces"]["items"] if metadata(x)["name"]]
        inv["namespaces"]["items"] = [metadata(x) for x in inv["namespaces"]["items"]]
        inv["serviceAccounts"]["items"] = [metadata(x) for x in inv["serviceAccounts"]["items"]]
        # Secret Table negotiation with includeObject=None exposes only server table cells, never Secret.data.
        secrets = []
        if ("", "v1", "secrets") in available and not inv["namespaces"].get("incomplete"):
            try:
                for namespace in ns:
                    rows, columns = self.pages(path_for("", "v1", "secrets", namespace), TABLE)
                    indexes = {c.get("name", "").lower(): i for i, c in enumerate(columns)}
                    if not {"name", "type"}.issubset(indexes):
                        raise AuditError("safe Secret Table response lacks NAME/TYPE columns")
                    for row in rows:
                        cells = row.get("cells", [])
                        get = lambda key: str(cells[indexes[key]]) if indexes[key] < len(cells) else ""
                        secrets.append({"namespace": namespace, "name": get("name"), "type": get("type"), "dataKeyCount": get("data")})
                inv["secrets"] = {"available": True, "items": secrets}
            except AuditError as exc:
                self.errors.append(f"secrets: {exc}")
                inv["secrets"] = {"available": True, "incomplete": True, "items": secrets}
        elif ("", "v1", "secrets") in available:
            inv["secrets"] = {"available": True, "incomplete": True, "items": []}
        else:
            inv["secrets"] = {"available": False, "items": []}
        for key in ("roles", "clusterRoles"):
            namespaced = key == "roles"
            inv[key]["items"] = [sanitize_role(x, namespaced) for x in inv[key]["items"]]
        for key in ("roleBindings", "clusterRoleBindings"):
            inv[key]["items"] = [sanitize_binding(x, key == "roleBindings") for x in inv[key]["items"]]
        inv["apiExports"]["items"] = [sanitize_export(x) for x in inv["apiExports"]["items"]]
        inv["apiBindings"]["items"] = [sanitize_api_binding(x) for x in inv["apiBindings"]["items"]]
        inv["apiResourceSchemas"]["items"] = [sanitize_schema(x) for x in inv["apiResourceSchemas"]["items"]]
        inv["apiExportEndpointSlices"]["items"] = [sanitize_endpoint_slice(x) for x in inv["apiExportEndpointSlices"]["items"]]
        inv["customResourceDefinitions"]["items"] = [sanitize_crd(x) for x in inv["customResourceDefinitions"]["items"]]
        for group, version, resource in sorted(OPTIONAL - {
            ("apis.kcp.io", "v1alpha1", "apiexports"),
            ("apis.kcp.io", "v1alpha2", "apiexports"),
            ("apis.kcp.io", "v1alpha1", "apibindings"),
            ("apis.kcp.io", "v1alpha2", "apibindings"),
            ("apis.kcp.io", "v1alpha1", "apiresourceschemas"),
            ("apis.kcp.io", "v1alpha1", "apiexportendpointslices"),
            ("apiextensions.k8s.io", "v1", "customresourcedefinitions")}):
            if (group, version, resource) in available:
                inv[resource] = self.list(resource, group, version, resource)
                inv[resource]["items"] = [metadata(x) for x in inv[resource]["items"]]
            else:
                inv[resource] = {"available": False, "items": []}
        analysis = analyze(inv)
        for key, inventory in inv.items():
            if any(item.get("inventoryTruncated") for item in inventory.get("items", [])):
                self.errors.append(f"{key}: nested inventory fields exceeded the report bound and were truncated")
                analysis["unresolvedEffects"].append(f"{key} contains truncated nested fields")
        if analysis.get("truncated"):
            self.errors.append("RBAC principal rule unions exceeded the report bound and were truncated")
            analysis["unresolvedEffects"].append("one or more principal rule unions are truncated")
        if any(x.get("aggregationRulePresent") for x in inv["clusterRoles"]["items"]):
            self.errors.append("ClusterRole aggregation rules are present; their selector-derived effective rules cannot be certified")
            analysis["unresolvedEffects"].append("aggregated ClusterRole rules can change outside the listed static rules")
        result = {"schemaVersion": 1, "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  "scope": {"workspace": self.workspace, "logicalClusterPath": actual_path}, "complete": not self.errors, "errors": sorted(set(self.errors)),
                  "limits": {"maxObjects": self.maximum, "maxSeconds": self.seconds, "pageSize": self.page,
                             "maxResponseBytes": MAX_RESPONSE, "maxTotalResponseBytes": MAX_TOTAL_RESPONSE},
                  "apiAvailability": api_availability(self.discovery),
                  "inventories": inv, "analysis": analysis,
                  "counts": {"objectsRead": self.read, "namespaces": len(ns),
                             "serviceAccounts": len(inv["serviceAccounts"]["items"]),
                             "serviceAccountTokenSecrets": sum(s["type"] == "kubernetes.io/service-account-token" for s in secrets)}}
        return result


def sanitize_role(x, namespaced):
    m = metadata(x)
    raw_rules = x.get("rules") or []
    rules = [sanitize_rule(rule) for rule in raw_rules[:500]]
    truncated = len(raw_rules) > len(rules) or any(rule.get("inventoryTruncated") for rule in rules)
    return {"name": m["name"], **({"namespace": m["namespace"]} if namespaced else {}),
            "labels": m["labels"], "rules": rules, "aggregationRulePresent": bool(x.get("aggregationRule")),
            **({"inventoryTruncated": True} if truncated else {})}


def sanitize_binding(x, namespaced):
    m = metadata(x)
    raw_subjects = x.get("subjects") or []
    subjects = [{k: s.get(k) for k in ("kind", "name", "namespace", "apiGroup") if s.get(k) is not None}
                for s in raw_subjects[:500]]
    return {"name": m["name"], "uid": m["uid"], **({"namespace": m["namespace"]} if namespaced else {}),
            "labels": m["labels"], "roleRef": x.get("roleRef", {}), "subjects": subjects,
            **({"inventoryTruncated": True} if len(raw_subjects) > len(subjects) else {}),
            "retiredDelegationBinding": any(s.get("kind") == "ServiceAccount" and
                s.get("namespace", "default") == "default" and s.get("name", "").startswith(RETIRED) for s in subjects)}


def sanitize_export(x):
    spec = x.get("spec") or {}
    raw_resources, raw_claims = spec.get("resources") or [], spec.get("permissionClaims") or []
    resources = [{k: entry.get(k) for k in ("group", "name", "identityHash") if entry.get(k) is not None}
                 for entry in raw_resources[:300]]
    claims = []
    for claim in raw_claims[:300]:
        entry = {k: claim.get(k) for k in ("group", "resource", "identityHash") if claim.get(k) is not None}
        verbs, clipped = clip(claim.get("verbs"), 100)
        if verbs:
            entry["verbs"] = verbs
        if clipped:
            entry["inventoryTruncated"] = True
        claims.append(entry)
    truncated = len(raw_resources) > len(resources) or len(raw_claims) > len(claims) or any(c.get("inventoryTruncated") for c in claims)
    return {"name": metadata(x)["name"], "resources": resources, "permissionClaims": claims,
            **({"inventoryTruncated": True} if truncated else {})}


def sanitize_api_binding(x):
    m, spec, status = metadata(x), x.get("spec") or {}, x.get("status") or {}
    export = ((spec.get("reference") or {}).get("export") or {})
    def claims(raw, states=False):
        result = []
        for wrapper in (raw or [])[:300]:
            claim = wrapper.get("permissionClaim", wrapper) if states else wrapper
            item = {k: claim.get(k) for k in ("group", "resource") if claim.get(k) is not None}
            verbs, clipped = clip(claim.get("verbs"), 100)
            if verbs:
                item["verbs"] = verbs
            item["selectorPresent"] = bool(claim.get("resourceSelector"))
            if states and "state" in wrapper:
                item["state"] = wrapper["state"]
            if clipped:
                item["inventoryTruncated"] = True
            result.append(item)
        return result, len(raw or []) > len(result) or any(c.get("inventoryTruncated") for c in result)
    accepted, accepted_truncated = claims(spec.get("permissionClaims"), True)
    applied, applied_truncated = claims(status.get("appliedPermissionClaims"))
    exported, exported_truncated = claims(status.get("exportPermissionClaims"))
    return {"name": m["name"], "uid": m["uid"], "terminating": bool((x.get("metadata") or {}).get("deletionTimestamp")),
            "export": {k: export.get(k) for k in ("path", "name") if export.get(k) is not None},
            "phase": status.get("phase", ""), "acceptedClaims": accepted,
            "appliedClaims": applied, "exportClaims": exported,
            **({"inventoryTruncated": True} if accepted_truncated or applied_truncated or exported_truncated else {})}


def preferred_api_version(available, resource):
    return next((v for v in ("v1alpha2", "v1alpha1") if ("apis.kcp.io", v, resource) in available), None)


def sanitize_schema(x):
    spec, status = x.get("spec") or {}, x.get("status") or {}
    names = spec.get("names") or {}
    raw_versions, raw_conditions = spec.get("versions") or [], status.get("conditions") or []
    return {"name": metadata(x)["name"], "group": spec.get("group", ""), "plural": names.get("plural", ""),
            "kind": names.get("kind", ""), "versions": [v.get("name", "") for v in raw_versions[:100]],
            "conditions": [{k: c.get(k) for k in ("type", "status", "reason") if k in c} for c in raw_conditions[:50]],
            **({"inventoryTruncated": True} if len(raw_versions) > 100 or len(raw_conditions) > 50 else {})}


def sanitize_endpoint_slice(x):
    spec = x.get("spec") or {}
    raw_endpoints = spec.get("endpoints") or []
    endpoints = []
    for endpoint in raw_endpoints[:100]:
        conditions = endpoint.get("conditions", [])
        endpoints.append({**safe_destination(endpoint.get("url", "")),
                          "conditions": conditions[:20],
                          **({"inventoryTruncated": True} if len(conditions) > 20 else {})})
    truncated = len(raw_endpoints) > 100 or any(e.get("inventoryTruncated") for e in endpoints)
    return {"name": metadata(x)["name"], "endpoints": endpoints,
            **({"inventoryTruncated": True} if truncated else {})}


def clip(items, limit):
    items = items or []
    return items[:limit], len(items) > limit


def sanitize_rule(rule):
    result, truncated = {}, False
    for key in ("apiGroups", "resources", "verbs", "resourceNames", "nonResourceURLs"):
        values, clipped = clip(rule.get(key), 1000)
        if values:
            result[key] = values
        truncated = truncated or clipped
    if truncated:
        result["inventoryTruncated"] = True
    return result


def sanitize_crd(x):
    spec = x.get("spec") or {}
    conv = spec.get("conversion") or {}
    webhook = (conv.get("webhook") or {}).get("clientConfig") or {}
    svc = webhook.get("service") or {}
    return {"name": metadata(x)["name"], "group": spec.get("group", ""), "scope": spec.get("scope", ""),
            "conversionStrategy": conv.get("strategy", ""),
            "conversionWebhook": {**safe_destination(webhook.get("url", "")),
                                  "service": {k: svc.get(k) for k in ("namespace", "name", "path", "port") if k in svc}} if conv.get("strategy") == "Webhook" else None}


def safe_destination(value):
    try:
        parsed = urlsplit(value)
        return {"destinationPresent": bool(parsed.scheme or parsed.netloc), "scheme": parsed.scheme or None,
                "hostname": parsed.hostname, "port": parsed.port}
    except ValueError:
        return {"destinationPresent": bool(value), "scheme": None, "hostname": None, "port": None}


def api_availability(discovery):
    expected = REQUIRED | OPTIONAL | {("authentication.k8s.io", v, "") for v in ("v1", "v1beta1")}
    versions = {(g, v) for g, v, _ in expected}
    versions.update((g, v) for g, v in discovery if g in {"authentication.k8s.io", "admissionregistration.k8s.io"})
    result = []
    for group, version in sorted(versions):
        doc = discovery.get((group, version))
        result.append({"group": group or "core", "version": version, "available": doc is not None,
                       "resources": sorted(r.get("name", "") for r in (doc or {}).get("resources", []))})
    return result


def risks(rule):
    groups, resources, verbs, urls = (rule.get(k) or [] for k in ("apiGroups", "resources", "verbs", "nonResourceURLs"))
    flags = set()
    if "*" in groups or "*" in resources or "*" in verbs:
        flags.add("wildcard-rule")
    if any(g in CONTROL_GROUPS or g.endswith((".kcp.io", ".k8s.io")) for g in groups):
        flags.add("control-plane-api-group")
    if any(v in CONTROL_VERBS for v in verbs):
        flags.add("identity-or-rbac-escalation-verb")
    if urls:
        flags.add("broad-nonresource-access" if "/" in urls or "*" in urls or any(u.endswith("*") for u in urls) else "nonresource-access")
    if "" in groups and any(r in resources for r in ("secrets", "serviceaccounts/token", "namespaces")):
        flags.add("sensitive-core-resource")
    return sorted(flags)


def analyze(inv):
    roles, crs = inv["roles"]["items"], inv["clusterRoles"]["items"]
    role_map = {("Role", r.get("namespace", ""), r["name"]): r for r in roles}
    role_map.update({("ClusterRole", "", r["name"]): r for r in crs})
    principals, unresolved, retired = {}, [], []
    unresolved_effects = set()
    truncated, rule_count, edge_count = False, 0, 0

    def entry_for(principal):
        nonlocal truncated
        if principal in principals:
            return principals[principal]
        if len(principals) >= MAX_ANALYSIS_PRINCIPALS:
            truncated = True
            return None
        principals[principal] = {"bindings": [], "rules": [], "ruleCount": 0, "riskFlags": set(), "truncated": False}
        return principals[principal]

    def append_limited(entry, key, value, limit=500):
        nonlocal truncated
        if len(entry[key]) >= limit:
            truncated = True
            entry["truncated"] = True
            return
        entry[key].append(value)

    def add_rules(entry, rules, scope):
        nonlocal truncated, rule_count
        for rule in rules:
            entry["ruleCount"] += 1
            if len(entry["rules"]) >= 500 or rule_count >= MAX_ANALYSIS_RULES:
                truncated = True
                entry["truncated"] = True
                break
            entry["rules"].append({"scope": scope, **rule})
            rule_count += 1

    for key, kind in (("roleBindings", "RoleBinding"), ("clusterRoleBindings", "ClusterRoleBinding")):
        for b in inv[key]["items"]:
            if edge_count >= MAX_ANALYSIS_EDGES:
                truncated = True
                break
            namespace = b.get("namespace", "") if kind == "RoleBinding" else ""
            ref = b.get("roleRef", {})
            role = role_map.get((ref.get("kind", ""), namespace if ref.get("kind") == "Role" else "", ref.get("name", "")))
            desc = {"kind": kind, "name": b.get("name", ""), "namespace": namespace, "roleRef": ref}
            if not role:
                unresolved.append(desc)
                continue
            rules = role.get("rules", [])
            flags = {f for r in rules for f in risks(r)}
            if role.get("aggregationRulePresent"):
                flags.add("aggregated-cluster-role")
            for s in b.get("subjects", []):
                if edge_count >= MAX_ANALYSIS_EDGES:
                    truncated = True
                    break
                edge_count += 1
                sk, name = s.get("kind", ""), s.get("name", "")
                sns = s.get("namespace", namespace if sk == "ServiceAccount" else "")
                principal = f"{sk}:{sns}:{name}" if sk == "ServiceAccount" else f"{sk}:{name}"
                e = entry_for(principal)
                if e is None:
                    continue
                append_limited(e, "bindings", desc)
                add_rules(e, rules, "cluster" if kind == "ClusterRoleBinding" else f"namespace:{namespace}")
                e["riskFlags"].update(flags)
                if sk == "ServiceAccount" and sns == "default" and name.startswith(RETIRED):
                    if len(retired) < MAX_ANALYSIS_PRINCIPALS:
                        retired.append({"principal": principal, **desc})
                    else:
                        truncated = True
                if sk == "Group" and name in {"system:masters", "system:authenticated"}:
                    e["riskFlags"].add("broad-system-group-subject")
                if sk == "Group":
                    if name == "system:authenticated":
                        unresolved_effects.add("system:authenticated also grants to non-ServiceAccount identities not enumerable in this workspace")
                    elif name not in {"system:serviceaccounts"} and not name.startswith("system:serviceaccounts:"):
                        unresolved_effects.add(f"membership of group {name!r} is not enumerable by this inventory")
        if truncated:
            break
    # Native ServiceAccounts also belong to the system service-account groups.
    for group, target in list(principals.items()):
        if not group.startswith("Group:"):
            continue
        name = group.removeprefix("Group:")
        if name not in {"system:serviceaccounts", "system:authenticated"} and not name.startswith("system:serviceaccounts:"):
            continue
        for account in inv.get("serviceAccounts", {}).get("items", []):
            if edge_count >= MAX_ANALYSIS_EDGES:
                truncated = True
                break
            edge_count += 1
            namespace, sa_name = account.get("namespace", ""), account.get("name", "")
            if name.startswith("system:serviceaccounts:") and name != f"system:serviceaccounts:{namespace}":
                continue
            principal = f"ServiceAccount:{namespace}:{sa_name}"
            entry = entry_for(principal)
            if entry is None:
                continue
            for binding in target["bindings"]:
                append_limited(entry, "bindings", binding)
            entry["ruleCount"] += target["ruleCount"]
            for rule in target["rules"]:
                if len(entry["rules"]) >= 500 or rule_count >= MAX_ANALYSIS_RULES:
                    truncated = True
                    entry["truncated"] = True
                    break
                entry["rules"].append(rule)
                rule_count += 1
            entry["riskFlags"].update(target["riskFlags"])
            entry["truncated"] = entry["truncated"] or target["truncated"]
        if truncated:
            break
    return {"principals": [{"principal": p, "bindings": e["bindings"], "effectiveRuleCount": e["ruleCount"],
                            "effectiveRules": e["rules"], "riskFlags": sorted(e["riskFlags"]),
                            **({"inventoryTruncated": True} if e["truncated"] or e["ruleCount"] > len(e["rules"]) else {})}
                           for p, e in sorted(principals.items())],
            "unresolvedRoleReferences": unresolved, "retiredDelegationBindings": retired,
            "unresolvedEffects": sorted(unresolved_effects), "truncated": truncated}


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--kubeconfig", required=True, type=Path, help="explicit KCP/API front-proxy kubeconfig")
    p.add_argument("--workspace", required=True, type=workspace_arg, help="exact team or org-provider path under root:railgrid:tenants:<org>")
    p.add_argument("--page-size", type=int, default=PAGE_SIZE)
    p.add_argument("--max-objects", type=int, default=MAX_OBJECTS)
    p.add_argument("--max-seconds", type=int, default=MAX_SECONDS)
    a = p.parse_args(argv)
    if not a.kubeconfig.is_file(): p.error("--kubeconfig must be an existing file")
    if not 1 <= a.page_size <= 500: p.error("--page-size must be 1..500")
    if not 1 <= a.max_objects <= 50000: p.error("--max-objects must be 1..50000")
    if not 5 <= a.max_seconds <= 300: p.error("--max-seconds must be 5..300")
    try:
        server = target_server(selected_server(a.kubeconfig), a.workspace)
        with Proxy(a.kubeconfig, server) as proxy:
            result = Scan(proxy, a.workspace, a.page_size, a.max_objects, a.max_seconds).build()
    except AuditError as exc:
        result = {"schemaVersion": 1, "scope": {"workspace": a.workspace}, "complete": False, "errors": [str(exc)]}
    raw = (json.dumps(result, indent=2, sort_keys=True) + "\n").encode()
    if len(raw) > MAX_REPORT:
        result = {"schemaVersion": 1, "scope": {"workspace": a.workspace}, "complete": False,
                  "errors": [f"report exceeded {MAX_REPORT} bytes; details omitted"]}
        raw = (json.dumps(result, indent=2, sort_keys=True) + "\n").encode()
    sys.stdout.buffer.write(raw)
    return 0 if result.get("complete") else 2


if __name__ == "__main__":
    raise SystemExit(main())
