# KCP requirements for hosted BYO providers

This is an upstream issue record, not a KCP implementation or fork. Railgrid
keeps hosted BYO enrollment unavailable through a source-controlled release
gate until a supported dependency meets these requirements. Do not patch the
module cache or build against a locally modified KCP dependency to claim
readiness.

## Cluster-scoped ResourceQuota selection

The current `k8s.io/apiserver` replacement is
`github.com/kcp-dev/kubernetes/staging/src/k8s.io/apiserver`
at `v0.0.0-20260602065202-e006560fc76a`.
In `pkg/admission/plugin/resourcequota/resource_access.go`, `GetQuotas("")`
selects quotas through the namespace-all lister and then appends annotated
admin-namespace quotas. The same quota can be charged twice. The second status
update conflicts; retries can repeatedly charge the first update until an empty
provider catalog appears full. Unrelated ordinary namespace quotas can also be
selected for a cluster-scoped request.

This was observed during real local enrollment and reproduced by the
Railgrid-owned native admission regression, including resource-version checks:

```sh
make test-kcp-quota-diagnostic
```

The diagnostic test has the `kcp_quota_diagnostic` build tag so the intentionally
failing upstream regression does not fail ordinary Railgrid unit acceptance.
The explicit diagnostic remains mandatory release acceptance and is expected to
fail on the current pin. A release must run it against the supported, unmodified
dependency and must not open the source-controlled hosted-enrollment gate unless
it passes. The ordinary test targets and native-guard suite do not establish
that quota selection is correct.

Required upstream behavior:

- A cluster-scoped request selects only applicable cluster-scoped quotas.
- Every quota is charged once, including requests in the admin namespace.
- Quota identity includes namespace and name, including conflict retry handling.
- Ordinary namespace quotas and genuinely distinct same-name quotas still work.

Keep the diagnostic and real enrollment check when adopting an upstream release.
An increased quota or a reset usage counter is not a correction.

## Logical-cluster total-object accounting

The current KCP replacement is `github.com/mjudeikis/kcp` at
`v0.0.0-20261028080645-9790f69ef65f`. Its
`pkg/admission/objectcountlimit/objectcountlimit_admission.go` decreases the
object registry during DELETE validation before persistence, with no dry-run
check. Source review also found separate count/check/increment steps and scanner
delta replacement. Repeated dry-run or finalizer-held deletion can therefore
undercount without deleting stored objects; concurrent admission needs separate
verification. These additional cases are source findings, not executed runtime
reproductions in this checkout.

Do not use `--logical-cluster-total-object-limit` or its workspace annotation as
a hard tenant-security boundary without upstream validation of dry-run handling,
successful persistence, concurrency and scanner reconciliation. Railgrid's
current hard count controls use the ResourceQuota mechanism instead.

## Scope of Railgrid implementation

Railgrid-owned admission plugins and authenticated request guards may use KCP's
existing exported extension points. Such adapters belong to this repository and
must be tested against the unmodified dependency. They must not silently replace
KCP implementations or declare external shards supported without equivalent
enforcement. Per-process traffic limits require a supported deployment topology;
they are not distributed storage or rate accounting.
