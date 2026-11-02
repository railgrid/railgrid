# Historical workspace credential audit

Use this report-only tool before trusting a legacy Railgrid team or org-provider
workspace. It inspects one exact team or org-provider workspace at a time; it does not enumerate
organizations, mutate the cluster, or certify the deployment as safe.

```bash
python3 hack/audit-historical-credentials.py \
  --kubeconfig /path/to/operator.kubeconfig \
  --workspace root:railgrid:tenants:<org>:<team> \
  > credential-audit.json
```

For an organization-owned provider runtime, use its exact provider workspace:

```bash
python3 hack/audit-historical-credentials.py \
  --kubeconfig /path/to/operator.kubeconfig \
  --workspace root:railgrid:tenants:<org>:providers:<provider> \
  > provider-credential-audit.json
```

The tool requires `kubectl` and an explicit kubeconfig whose current context
addresses the KCP/API front proxy. It validates the workspace path, replaces
any existing `/clusters/<id>` server suffix with the requested workspace path,
then verifies that the returned LogicalCluster's `kcp.io/path` exactly matches
before listing inventory. A mismatch stops the scan. The temporary kubectl
proxy binds to loopback, accepts only API paths, rejects write methods, and is
terminated on exit.

The report includes namespace, ServiceAccount, Role/ClusterRole,
RoleBinding/ClusterRoleBinding, APIExport and APIBinding, APIResourceSchema,
APIExportEndpointSlice, CRD conversion, and discovered admission/webhook API
inventory. It records authentication API discovery (including TokenReview
availability) when the API version is published in that workspace, without
creating reviews. RBAC analysis resolves each binding
against workspace Roles and ClusterRoles, reports unresolved role references,
and computes each directly bound principal's union of rules. It also applies
`system:serviceaccounts`, namespace-specific service-account groups, and
`system:authenticated` grants to the known ServiceAccounts. Unenumerable group
membership and aggregated ClusterRole selectors appear as unresolved effects;
aggregation makes the overall scan incomplete.

Secret collection uses Kubernetes Table negotiation with
`includeObject=None`, reporting only namespace, name, type, and data-key count.
ServiceAccount collection uses PartialObjectMetadata. There is no fallback to a
full Secret object if the server does not support these responses. The script
never emits Secret data or unapproved annotations. Store the resulting report
as sensitive operational metadata: it contains principal names, permission
rules, workspace API names, and webhook destinations.

APIBinding output is a bounded projection of the export path/name, phase,
termination state, and permission-claim group/resource/state. The pinned KCP
claim type has no verbs field. Selector contents are omitted; only their
presence is reported. Admission and webhook
inventory reports discovered API availability and object names only. They do
not include policy rules or webhook client configuration; an authorized
operator must inspect those configurations separately. These inventories do
not cover shard-global configuration outside the selected workspace.

The default bounds are 100 objects per page, 5,000 list objects total, a 60
second scan budget, an 8 second request timeout, a 4 MiB response cap, a 64 MiB
cumulative response cap, and an 8 MiB report cap. Derived RBAC analysis also
caps principal counts and rule expansion; reaching any bound marks the report
incomplete. Use
`--page-size`, `--max-objects`, or `--max-seconds` to lower the bounds when
needed. Exit status `0` means the requested inventories completed; status `2`
means at least one API, permission, limit, scope check, or aggregation made the
report incomplete. A partial JSON report is still written to stdout. Do not
treat an incomplete report as an empty or safe inventory.

## Interpreting provenance

The retired `default/railgrid-du-*` namespace is identifiable by its reserved
name prefix. Embedded KCP and the hub reject authenticated credentials in that
prefix, but the ServiceAccounts and paired bindings remain until explicitly
removed. `railgrid-pdu-*` is the current platform delegation namespace. Other
Railgrid workload identities carry managed markers and a central identity
record. Labels and annotations can be forged by a workspace administrator;
where available, only the hub's keyed ServiceAccount proof and the live
identity record establish that the hub issued the identity. Ordinary Service
Account names, token Secret types, and RBAC objects do not establish who
created them.

An old cluster-admin runtime could create additional ServiceAccounts, tokens,
roles, and bindings. The report can show the currently stored objects and their
effective workspace RBAC, but it cannot reconstruct an issuer when historical
audit records are absent. It also cannot find credentials copied outside KCP.
It does not inspect hub static tokens, OIDC provider secrets, browser/session
credentials, certificate signing/issuance history, ingress credentials, external
KCP shards, or cluster-wide API
server authentication/admission configuration. Audit those separately and
rotate identity-provider or hub credentials when their exposure is uncertain.
KCP audit webhook configuration records future events; it is not historical
credential provenance.

Broad rules, `impersonate`/`bind`/`escalate`, control-plane API groups, system
groups, unresolved role references, retired-prefix identities, and
conversion/webhook declarations are review findings, not automatic proof of
cross-organization access. Confirm the workspace's accepted APIExport claims,
the current Railgrid boundary, and any external shard's own policy before
concluding impact. Hosted enrollment remains source-gated for the known KCP quota regression.
Completing this operator audit does not enable enrollment or resolve that
independent upstream blocker.

## Explicit cleanup of a retired-prefix identity

Never feed report contents into bulk deletion. For one reviewed
`railgrid-du-*` ServiceAccount, record the exact ServiceAccount UID and the UID
of its paired ClusterRoleBinding only after confirming the binding's subject is
that ServiceAccount in namespace `default`. Use the team's exact workspace
server and delete the binding first, then the ServiceAccount, each with its UID
precondition. This prevents deleting a replacement object that reused a name.

In one terminal, start a temporary proxy using an operator kubeconfig and the
exact workspace server (the path shown in the report):

```bash
kubectl --kubeconfig "$KUBECONFIG_FILE" \
  --server "$KCP_WORKSPACE_SERVER" \
  proxy --address=127.0.0.1 --port=8001 --append-server-path=true
```

After substituting individually reviewed names and UIDs, issue these requests
in a second terminal. `KCP_WORKSPACE_SERVER` must be the explicit
`https://.../clusters/root:railgrid:tenants:<org>:<team>` or
`...:providers:<name>` URL.

```bash
curl --fail-with-body -X DELETE \
  -H 'Content-Type: application/json' \
  --data '{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"<reviewed-binding-uid>"}}' \
  'http://127.0.0.1:8001/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/<reviewed-binding-name>'

curl --fail-with-body -X DELETE \
  -H 'Content-Type: application/json' \
  --data '{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"<reviewed-serviceaccount-uid>"}}' \
  'http://127.0.0.1:8001/api/v1/namespaces/default/serviceaccounts/<reviewed-serviceaccount-name>'
```

Stop the proxy after these reviewed requests. If the paired binding cannot be
proven to reference the retired account, or the UID precondition conflicts,
stop and re-inventory rather than broadening the deletion. Do not delete
arbitrary child ServiceAccounts or roles based on their age, labels, or names;
their provenance and dependencies require operator investigation.
