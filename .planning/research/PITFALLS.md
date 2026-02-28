# PITFALLS.md — Edge Refactor Pitfalls & Prevention

## 1. Deep copy not regenerated

**Risk:** After changing/adding types in `types_edge.go`, the `zz_generated.deepcopy.go` file will be stale. The Go compiler won't catch this — it will silently share pointers (e.g., `EdgeStatus.Conditions`) across objects.

**Prevention:**
- Run `make generate` (or `controller-gen object:headerFile=...`) immediately after changing types
- Verify `zz_generated.deepcopy.go` is committed in the same PR as type changes
- Phase 1 checklist item: `go vet ./apis/...` passes

---

## 2. Stray references to `Site` / `Server` after deletion

**Risk:** Go's type system will catch direct references, but string-based references (CRD name in manifests, RBAC rules, URL path segments, test fixtures, README docs) will compile fine and break at runtime.

**Prevention:**
- After deletion, run: `grep -r '"sites"\|"servers"\|Site{}\|Server{}\|SiteName\|ServerName\|--mode' --include='*.go' .`
- Also check: `grep -r 'site\|server' test/e2e/ --include='*.go'` for test fixtures
- Phase 5 plan explicitly includes a grep sweep

---

## 3. connman key collision

**Risk:** The old code used `{cluster}/servers/{name}` for servers to avoid aliasing. The new code uses `{cluster}/{name}` for all edges. If the name of an edge collides with a legacy key still in the connman (e.g., during a partial migration in tests), connections will be misrouted.

**Prevention:**
- The full refactor removes both old key formats in one shot — there's no migration period
- In e2e tests, ensure test teardown clears connman entries between runs
- connman unit tests (`connman_test.go`) cover key uniqueness

---

## 4. Mount reconciler creates workspace for `type=server` edges

**Risk:** Accidentally copying the mount reconciler without adding the `type=kubernetes` guard will try to create kcp workspaces for SSH-only edges, which is wasted work and may fail if kcp isn't configured.

**Prevention:**
- Add guard at the top of `Reconcile` in `edge/mount_reconciler.go`:
  ```go
  if edge.Spec.Type != kedgev1alpha1.EdgeTypeKubernetes {
      return ctrl.Result{}, nil
  }
  ```
- Unit test: `TestMountReconciler_SkipsServer` — reconcile an Edge with `type=server`, assert no kcp calls

---

## 5. Agent `--type` vs `--mode` flag rename breaks existing deployments

**Risk:** Any existing `kedge-agent` invocations using `--mode=site` or `--mode=server` will silently fail or use the default (kubernetes) if the old flag is removed.

**Prevention:**
- Add a deprecated `--mode` alias that prints a warning and maps to `--type`
- Or: accept both `--mode` and `--type` via cobra's `MarkDeprecated`
- Document migration in commit message / PR description

---

## 6. CLI `kedge ssh` URL path construction

**Risk:** The old URL is `/proxy/apis/kedge.faros.sh/v1alpha1/{sites|servers}/{name}/ssh`. The new URL is `/proxy/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh`. Building the URL wrong (e.g., keeping `sites` as default) causes a 404 that looks like a connection issue.

**Prevention:**
- Delete `resolveResourceKind` — if it compiles, it should be gone
- Add a simple unit test for `buildSSHWebSocketURL` asserting it produces `edges/{name}/ssh`

---

## 7. Authorization SAR checks still reference `sites`/`servers`

**Risk:** SubjectAccessReview checks in `edges_proxy_builder.go` must use resource=`edges`, not the old names. A leftover `resource = "sites"` string will cause all authorization to fail.

**Prevention:**
- Search for `"sites"` and `"servers"` strings in virtual builder after phase 3
- Integration test in e2e: verify OIDC + SA tokens are authorized against `edges`

---

## 8. CRD bootstrap — old CRDs not deleted from embedded FS

**Risk:** `pkg/hub/bootstrap/crds/` uses `embed.FS`. If old `sites.yaml` / `servers.yaml` manifests remain in the embedded directory, the hub will still try to apply them on startup, creating zombie CRDs.

**Prevention:**
- Delete the files from the filesystem (not just stop referencing them)
- Phase 1 checklist: confirm `ls pkg/hub/bootstrap/crds/` shows only `edges` (and other non-site CRDs)
