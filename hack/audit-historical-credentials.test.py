import importlib.util
import re
import unittest
from unittest.mock import patch
from pathlib import Path

spec = importlib.util.spec_from_file_location("credential_audit", Path(__file__).with_name("audit-historical-credentials.py"))
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class CredentialAuditTest(unittest.TestCase):
    def test_workspace_must_be_one_exact_team_path(self):
        self.assertEqual(audit.workspace_arg("root:railgrid:tenants:org-a:team-1"), "root:railgrid:tenants:org-a:team-1")
        self.assertEqual(audit.workspace_arg("root:railgrid:tenants:org-a:providers:sample"), "root:railgrid:tenants:org-a:providers:sample")
        for value in ("root", "root:railgrid:tenants:org-a", "root:railgrid:tenants:org-a:team-1:extra", "other:railgrid:tenants:a:b", "root:railgrid:tenants:org-a:providers"):
            with self.subTest(value=value), self.assertRaises(audit.argparse.ArgumentTypeError):
                audit.workspace_arg(value)

    def test_server_target_replaces_only_kcp_cluster_path(self):
        self.assertEqual(
            audit.target_server("https://kcp.example/clusters/root", "root:railgrid:tenants:o:t"),
            "https://kcp.example/clusters/root:railgrid:tenants:o:t",
        )
        with self.assertRaises(audit.AuditError):
            audit.target_server("https://kcp.example/unexpected/path", "root:railgrid:tenants:o:t")
        for server in ("https://user:pass@kcp.example", "https://kcp.example/?token=secret", "https://kcp.example/#secret"):
            with self.subTest(server=server), self.assertRaises(audit.AuditError):
                audit.target_server(server, "root:railgrid:tenants:o:t")

    def test_proxy_rejects_every_method_except_get(self):
        pattern = re.compile(audit.Proxy(Path("unused"), "https://kcp.example").cmd[-1].split("=", 1)[1])
        for method in ("POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "PROPFIND", "G", "GE", "GETTING"):
            with self.subTest(method=method):
                self.assertIsNotNone(pattern.fullmatch(method))
        self.assertIsNone(pattern.fullmatch("GET"))

    def test_destination_report_redacts_userinfo_path_and_query(self):
        got = audit.safe_destination("https://user:secret@example.test/private?token=hidden#fragment")
        self.assertEqual(got, {"destinationPresent": True, "scheme": "https", "hostname": "example.test", "port": None})
        self.assertNotIn("secret", repr(got))
        self.assertNotIn("hidden", repr(got))

    def test_metadata_projection_drops_secret_and_unapproved_annotations(self):
        item = {"metadata": {"name": "sa", "namespace": "default", "uid": "u", "annotations": {
            "railgrid.ai/delegated-user": "alice", "custom.example/token": "never-report",
        }, "labels": {"railgrid.ai/workload-identity": "true", "railgrid.ai/token": "never-report", "custom.example/label": "x"}}, "data": {"token": "never-read"}}
        got = audit.metadata(item)
        self.assertEqual(got["annotations"], {"railgrid.ai/delegated-user": "alice"})
        self.assertEqual(got["labels"], {"railgrid.ai/workload-identity": True})
        self.assertNotIn("data", got)
        self.assertNotIn("never-report", repr(got))
        self.assertNotIn("never-read", repr(got))

    def test_rule_risk_flags_escalation_wildcard_and_sensitive_core(self):
        flags = audit.risks({"apiGroups": ["*"], "resources": ["*"], "verbs": ["impersonate", "*"]})
        self.assertIn("wildcard-rule", flags)
        self.assertIn("identity-or-rbac-escalation-verb", flags)
        flags = audit.risks({"apiGroups": [""], "resources": ["serviceaccounts/token"], "verbs": ["create"]})
        self.assertIn("sensitive-core-resource", flags)
        flags = audit.risks({"nonResourceURLs": ["/api/*"], "verbs": ["get"]})
        self.assertIn("broad-nonresource-access", flags)

    def test_analysis_unions_principal_rules_and_flags_retired_and_unresolved(self):
        inv = {
            "roles": {"items": [{"name": "read", "namespace": "team", "rules": [{"apiGroups": [""], "resources": ["secrets"], "verbs": ["get"]}]}]},
            "clusterRoles": {"items": [{"name": "broad", "rules": [{"apiGroups": ["*"], "resources": ["*"], "verbs": ["*"]}]}]},
            "roleBindings": {"items": [{"name": "rb", "namespace": "team", "roleRef": {"kind": "Role", "name": "read"}, "subjects": [
                {"kind": "User", "name": "alice"}, {"kind": "ServiceAccount", "namespace": "team", "name": "runner"}]}]},
            "clusterRoleBindings": {"items": [
                {"name": "retired", "roleRef": {"kind": "ClusterRole", "name": "broad"}, "subjects": [{"kind": "ServiceAccount", "namespace": "default", "name": "railgrid-du-old"}]},
                {"name": "dangling", "roleRef": {"kind": "ClusterRole", "name": "missing"}, "subjects": [{"kind": "User", "name": "bob"}]},
                {"name": "serviceaccounts", "roleRef": {"kind": "ClusterRole", "name": "broad"}, "subjects": [{"kind": "Group", "name": "system:serviceaccounts:team"}]},
            ]},
            "serviceAccounts": {"items": [{"name": "runner", "namespace": "team"}]},
        }
        result = audit.analyze(inv)
        principals = {p["principal"]: p for p in result["principals"]}
        self.assertEqual(principals["User:alice"]["effectiveRuleCount"], 1)
        self.assertIn("wildcard-rule", principals["ServiceAccount:default:railgrid-du-old"]["riskFlags"])
        self.assertEqual(len(result["retiredDelegationBindings"]), 1)
        self.assertEqual(result["unresolvedRoleReferences"][0]["name"], "dangling")
        self.assertIn("wildcard-rule", principals["ServiceAccount:team:runner"]["riskFlags"])
        self.assertEqual(principals["ServiceAccount:team:runner"]["effectiveRuleCount"], 2)

    def test_nested_inventory_caps_are_explicitly_marked_incomplete(self):
        role = {"metadata": {"name": "large"}, "rules": [{"apiGroups": ["group"]}] * 501}
        binding = {"metadata": {"name": "large"}, "subjects": [{"kind": "User", "name": str(i)} for i in range(501)]}
        sanitized_role = audit.sanitize_role(role, False)
        sanitized_binding = audit.sanitize_binding(binding, False)
        self.assertTrue(sanitized_role["inventoryTruncated"])
        self.assertTrue(sanitized_binding["inventoryTruncated"])
        self.assertTrue(audit.sanitize_rule({"verbs": ["get"] * 1001})["inventoryTruncated"])

        inv = {
            "roles": {"items": []}, "clusterRoles": {"items": [{"name": "large", "rules": [{"apiGroups": ["g"], "resources": ["r"], "verbs": ["get"]}] * 501}]},
            "roleBindings": {"items": []}, "clusterRoleBindings": {"items": [{"name": "many", "roleRef": {"kind": "ClusterRole", "name": "large"}, "subjects": [{"kind": "User", "name": "alice"}]}]},
            "serviceAccounts": {"items": []},
        }
        self.assertTrue(audit.analyze(inv)["truncated"])

    def test_apibinding_projection_is_bounded_and_redacts_selector_contents(self):
        binding = {"metadata": {"name": "binding", "uid": "u", "deletionTimestamp": "now"},
                   "spec": {"reference": {"export": {"path": "root:org:providers:p", "name": "p"}},
                            "permissionClaims": [{"group": "g", "resource": "widgets", "verbs": ["get"],
                                                  "resourceSelector": [{"name": "private-selector-secret"}], "state": "Accepted"}]},
                   "status": {"phase": "Bound", "appliedPermissionClaims": [{"group": "g", "resource": "widgets", "verbs": ["get"]}]}}
        got = audit.sanitize_api_binding(binding)
        self.assertTrue(got["terminating"])
        self.assertEqual(got["export"], {"path": "root:org:providers:p", "name": "p"})
        self.assertEqual(got["acceptedClaims"][0]["state"], "Accepted")
        self.assertTrue(got["acceptedClaims"][0]["selectorPresent"])
        self.assertNotIn("private-selector-secret", repr(got))
        binding["spec"]["permissionClaims"] *= 301
        self.assertTrue(audit.sanitize_api_binding(binding)["inventoryTruncated"])

    def test_api_export_and_binding_version_selection_prefers_v1alpha2(self):
        available = {("apis.kcp.io", "v1alpha1", "apibindings"),
                     ("apis.kcp.io", "v1alpha2", "apibindings")}
        self.assertEqual(audit.preferred_api_version(available, "apibindings"), "v1alpha2")
        self.assertIsNone(audit.preferred_api_version(set(), "apibindings"))

    def test_secret_table_request_excludes_object_payload(self):
        class FakeProxy:
            last_bytes = 0
            port = 1
            def get(self, path, accept, timeout):
                seen.append((path, accept))
                return {"kind": "Table", "rows": [{"cells": ["private", "kubernetes.io/service-account-token", 1], "object": None}],
                        "columnDefinitions": [{"name": "Name"}, {"name": "Type"}, {"name": "Data"}], "metadata": {}}

        seen = []
        scanner = audit.Scan(FakeProxy(), "root:railgrid:tenants:o:t", 10, 20, 10)
        rows, columns = scanner.pages("/api/v1/namespaces/default/secrets", audit.TABLE)
        self.assertIn(("/api/v1/namespaces/default/secrets?limit=10&includeObject=None", audit.TABLE), seen)
        self.assertEqual(rows[0]["cells"][1], "kubernetes.io/service-account-token")
        self.assertIsNone(rows[0]["object"])
        self.assertEqual(columns[0]["name"], "Name")

    def test_secret_table_rejects_embedded_object_data(self):
        class FakeProxy:
            last_bytes = 0
            def get(self, *_args):
                return {"kind": "Table", "rows": [{"cells": [], "object": {"data": {"token": "secret"}}}],
                        "columnDefinitions": [], "metadata": {}}
        scanner = audit.Scan(FakeProxy(), "root:railgrid:tenants:o:t", 10, 20, 10)
        with self.assertRaises(audit.AuditError):
            scanner.pages("/api/v1/namespaces/default/secrets", audit.TABLE)

    def test_analysis_budget_overflow_is_explicit(self):
        inv = {"roles": {"items": []}, "clusterRoles": {"items": [{"name": "read", "rules": [{"apiGroups": ["g"], "resources": ["r"], "verbs": ["get"]}]}]},
               "roleBindings": {"items": []}, "clusterRoleBindings": {"items": [{"name": "many", "roleRef": {"kind": "ClusterRole", "name": "read"},
                   "subjects": [{"kind": "User", "name": "a"}, {"kind": "User", "name": "b"}]}]}, "serviceAccounts": {"items": []}}
        with patch.object(audit, "MAX_ANALYSIS_EDGES", 1):
            self.assertTrue(audit.analyze(inv)["truncated"])


if __name__ == "__main__":
    unittest.main()
