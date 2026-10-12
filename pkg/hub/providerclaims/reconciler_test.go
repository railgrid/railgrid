/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providerclaims

import (
	"context"
	"errors"
	"testing"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

type fakeOps struct {
	paths map[string]string

	coreCalls     []string
	providerCalls []providerCall
	pathErr       error
}

type providerCall struct {
	cluster, binding string
	claims           []kcp.ProviderClaim
}

func (f *fakeOps) EnsureCoreBindingClaims(_ context.Context, clusterName, bindingName string) (bool, error) {
	f.coreCalls = append(f.coreCalls, clusterName+"/"+bindingName)
	return true, nil
}

func (f *fakeOps) ReconcileProviderBindingClaims(_ context.Context, clusterName, bindingName string, claims []kcp.ProviderClaim) (bool, error) {
	f.providerCalls = append(f.providerCalls, providerCall{cluster: clusterName, binding: bindingName, claims: claims})
	return true, nil
}

func (f *fakeOps) ResolveClusterPath(_ context.Context, clusterName string) (string, error) {
	if f.pathErr != nil {
		return "", f.pathErr
	}
	path, ok := f.paths[clusterName]
	if !ok {
		return "", errors.New("unknown cluster " + clusterName)
	}
	return path, nil
}

type fakeGrants struct {
	grants map[hubaccess.GrantKey]*tenancyv1alpha1.Grant
	err    error
	calls  int
}

func (f *fakeGrants) Get(_ context.Context, key hubaccess.GrantKey) (*tenancyv1alpha1.Grant, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.grants[key], nil
}

func binding(name, exportPath, exportName string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: exportPath, Name: exportName},
			},
		},
	}
}

func newReconciler(ops *fakeOps, reg *providers.Registry, grants GrantReader) *Reconciler {
	return &Reconciler{ops: ops, registry: reg, grants: grants, paths: map[string]string{}}
}

func TestReconcileBinding_CoreBindingGoesToEmbeddedClaims(t *testing.T) {
	ops := &fakeOps{}
	r := newReconciler(ops, providers.NewRegistry(), nil)

	if _, err := r.reconcileBinding(context.Background(), "c1", binding(kcp.CoreBindingName, kcppaths.SystemControllers, kcp.CoreExportName)); err != nil {
		t.Fatal(err)
	}
	if len(ops.coreCalls) != 1 || ops.coreCalls[0] != "c1/railgrid" {
		t.Fatalf("core calls = %v, want [c1/railgrid]", ops.coreCalls)
	}
	if len(ops.providerCalls) != 0 {
		t.Fatalf("provider writer called for the core binding: %+v", ops.providerCalls)
	}
}

func TestReconcileBinding_ProviderBindingGetsDesiredClaims(t *testing.T) {
	ops := &fakeOps{paths: map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")}}
	reg := providers.NewRegistry()
	prov := agentsProvider()
	reg.Upsert(prov)
	grants := &fakeGrants{grants: map[hubaccess.GrantKey]*tenancyv1alpha1.Grant{
		{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: "agents"}: grantWith(hubaccess.ComposeCapability("code.railgrid.ai", "repositories")),
	}}
	r := newReconciler(ops, reg, grants)

	if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", prov.APIExportPath, prov.APIExportName)); err != nil {
		t.Fatal(err)
	}
	if len(ops.providerCalls) != 1 {
		t.Fatalf("provider calls = %+v, want one", ops.providerCalls)
	}
	call := ops.providerCalls[0]
	if call.cluster != "c1" || call.binding != "agents" {
		t.Fatalf("call = %+v", call)
	}
	accepted := map[string]bool{}
	for _, c := range call.claims {
		accepted[Key(c.Group, c.Resource)] = c.Accepted
	}
	if !accepted["/secrets"] || !accepted["authentication.k8s.io/tokenreviews"] {
		t.Fatalf("own requirements not accepted: %+v", call.claims)
	}
	if !accepted["code.railgrid.ai/repositories"] {
		t.Fatalf("consented composition not accepted: %+v", call.claims)
	}
	if v, ok := accepted["code.railgrid.ai/repositories/sync"]; !ok || v {
		t.Fatalf("unconsented composition must be present and rejected: %+v", call.claims)
	}
	if grants.calls != 1 {
		t.Fatalf("grant reads = %d, want 1", grants.calls)
	}

	// The path is cached: a second reconcile does not resolve it again.
	ops.pathErr = errors.New("kcp unreachable")
	if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", prov.APIExportPath, prov.APIExportName)); err != nil {
		t.Fatalf("second reconcile should use the cached path: %v", err)
	}
}

func TestReconcileBinding_LeavesAloneWhatItDoesNotOwn(t *testing.T) {
	prov := agentsProvider()
	reg := providers.NewRegistry()
	reg.Upsert(prov)
	orgProv := prov
	orgProv.OrgUUID = "org-b"
	orgProv.APIExportPath = kcppaths.OrgProviderPath("org-b", "agents")
	reg.Upsert(orgProv)

	for _, tc := range []struct {
		name    string
		binding *apisv1alpha2.APIBinding
		paths   map[string]string
	}{
		{
			name: "terminating binding",
			binding: func() *apisv1alpha2.APIBinding {
				b := binding("agents", prov.APIExportPath, prov.APIExportName)
				now := metav1.Now()
				b.DeletionTimestamp = &now
				return b
			}(),
			paths: map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")},
		},
		{
			name:    "export of no registered provider",
			binding: binding("mine", "root:someone:else", "things.example.com"),
		},
		{
			name:    "platform export name that does not match the provider",
			binding: binding("agents", prov.APIExportPath, "other.railgrid.ai"),
			paths:   map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")},
		},
		{
			name:    "binding outside the tenant tree",
			binding: binding("agents", prov.APIExportPath, prov.APIExportName),
			paths:   map[string]string{"c1": kcppaths.ProviderPath("kuery")},
		},
		{
			// GetForOrg falls back to the platform provider; an Org path must
			// resolve to the Org's own record and nothing else.
			name:    "org export path for an org that has no such provider",
			binding: binding("agents", kcppaths.OrgProviderPath("org-c", "agents"), prov.APIExportName),
			paths:   map[string]string{"c1": kcppaths.WorkspacePath("org-c", "ws-1")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeOps{paths: tc.paths}
			r := newReconciler(ops, reg, nil)
			if _, err := r.reconcileBinding(context.Background(), "c1", tc.binding); err != nil {
				t.Fatal(err)
			}
			if len(ops.providerCalls) != 0 || len(ops.coreCalls) != 0 {
				t.Fatalf("binding was written: provider=%+v core=%v", ops.providerCalls, ops.coreCalls)
			}
		})
	}

	t.Run("org-owned export resolves to the org's own record", func(t *testing.T) {
		ops := &fakeOps{paths: map[string]string{"c1": kcppaths.WorkspacePath("org-b", "ws-1")}}
		r := newReconciler(ops, reg, nil)
		if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", orgProv.APIExportPath, orgProv.APIExportName)); err != nil {
			t.Fatal(err)
		}
		if len(ops.providerCalls) != 1 {
			t.Fatalf("org-owned binding not reconciled: %+v", ops.providerCalls)
		}
	})
}

func TestReconcileBinding_EmptyRequirementsClearOnlyOnceObserved(t *testing.T) {
	prov := agentsProvider()
	prov.Requires = nil

	t.Run("observed CatalogEntry declaring nothing clears the binding", func(t *testing.T) {
		reg := providers.NewRegistry()
		reg.Upsert(prov)
		ops := &fakeOps{paths: map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")}}
		r := newReconciler(ops, reg, nil)
		if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", prov.APIExportPath, prov.APIExportName)); err != nil {
			t.Fatal(err)
		}
		if len(ops.providerCalls) != 1 || len(ops.providerCalls[0].claims) != 0 {
			t.Fatalf("expected one write with an empty claim set, got %+v", ops.providerCalls)
		}
	})

	t.Run("registry record without an observed CatalogEntry is left alone", func(t *testing.T) {
		unobserved := prov
		unobserved.CatalogEntryCluster = ""
		reg := providers.NewRegistry()
		reg.Upsert(unobserved)
		ops := &fakeOps{paths: map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")}}
		r := newReconciler(ops, reg, nil)
		if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", prov.APIExportPath, prov.APIExportName)); err != nil {
			t.Fatal(err)
		}
		if len(ops.providerCalls) != 0 {
			t.Fatalf("binding rewritten from an unobserved declaration: %+v", ops.providerCalls)
		}
	})
}

func TestReconcileBinding_RetriesWhenConsentCannotBeRead(t *testing.T) {
	reg := providers.NewRegistry()
	prov := agentsProvider()
	reg.Upsert(prov)
	ops := &fakeOps{paths: map[string]string{"c1": kcppaths.WorkspacePath("org-a", "ws-1")}}
	r := newReconciler(ops, reg, &fakeGrants{err: errors.New("grants unavailable")})
	if _, err := r.reconcileBinding(context.Background(), "c1", binding("agents", prov.APIExportPath, prov.APIExportName)); err == nil {
		t.Fatal("expected an error so the binding is requeued, got nil")
	}
	if len(ops.providerCalls) != 0 {
		t.Fatalf("binding written without knowing the tenant's consent: %+v", ops.providerCalls)
	}
}

func TestRegistryNotifiesOnRequirementsChangeOnly(t *testing.T) {
	reg := providers.NewRegistry()
	var seen []string
	unregister := reg.OnRequirementsChange(func(p providers.Provider) { seen = append(seen, p.Name) })

	prov := agentsProvider()
	reg.Upsert(prov) // first observation
	reg.Upsert(prov) // same requirements: silent
	prov.Version = "2"
	reg.Upsert(prov) // unrelated field: silent
	prov.Requires = prov.Requires[:1]
	reg.Upsert(prov) // requirements changed

	if len(seen) != 2 {
		t.Fatalf("listener calls = %v, want exactly two (first observation and the change)", seen)
	}

	unregister()
	prov.Requires = nil
	reg.Upsert(prov)
	if len(seen) != 2 || reg.RequirementsListeners() != 0 {
		t.Fatalf("unregistered listener still called (%v) or still registered (%d)", seen, reg.RequirementsListeners())
	}
}

// The fan-out source subscribes for exactly one leadership term: Start
// registers, the term's context ending unregisters. A hub that flaps
// leadership must not accumulate dead listeners on the process-wide registry.
func TestRequirementsSourceSubscribesForOneTerm(t *testing.T) {
	reg := providers.NewRegistry()
	r := newReconciler(&fakeOps{}, reg, nil)
	src := &requirementsSource{r: r}

	ctx, cancel := context.WithCancel(context.Background())
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer q.ShutDown()
	if err := src.Start(ctx, q); err != nil {
		t.Fatal(err)
	}
	if reg.RequirementsListeners() != 1 {
		t.Fatalf("listeners after Start = %d, want 1", reg.RequirementsListeners())
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for reg.RequirementsListeners() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.RequirementsListeners() != 0 {
		t.Fatalf("listener survived the end of the term: %d registered", reg.RequirementsListeners())
	}
	// A notification after the term must be a no-op, not a goroutine on a
	// dead queue.
	src.notify(agentsProvider())
	if q.Len() != 0 {
		t.Fatalf("queue received work after the term ended: %d", q.Len())
	}
}
