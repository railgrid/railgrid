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

package workspaceseeds

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func validSpec() adminv1alpha1.WorkspaceSeedSpec {
	return adminv1alpha1.WorkspaceSeedSpec{
		Bindings: []adminv1alpha1.SeedBinding{{
			Name:   "switchboard",
			Export: adminv1alpha1.SeedExportReference{Path: "root:railgrid:system:switchboard", Name: "switchboard.railgrid.ai"},
			PermissionClaims: []adminv1alpha1.SeedPermissionClaim{{
				Resource: "secrets", Verbs: []string{"get"},
				Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "switchboard.railgrid.ai/request", Operator: metav1.LabelSelectorOpExists}}},
			}},
			TenantAccess: adminv1alpha1.SeedTenantAccessReadOnly,
		}},
		Objects: []adminv1alpha1.SeedObject{{
			Resource: "requests",
			Manifest: runtime.RawExtension{Raw: []byte(`{"apiVersion":"switchboard.railgrid.ai/v1alpha1","kind":"Request","metadata":{"name":"free","namespace":"default"}}`)},
		}},
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(ptr(validSpec())); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(*adminv1alpha1.WorkspaceSeedSpec)
		want   string
	}{
		"core binding name": {func(s *adminv1alpha1.WorkspaceSeedSpec) { s.Bindings[0].Name = kcp.CoreBindingName }, "hub's own binding"},
		"duplicate binding": {func(s *adminv1alpha1.WorkspaceSeedSpec) { s.Bindings = append(s.Bindings, s.Bindings[0]) }, "duplicate name"},
		"unknown access":    {func(s *adminv1alpha1.WorkspaceSeedSpec) { s.Bindings[0].TenantAccess = "readonly" }, "unknown tenantAccess"},
		"every secret":      {func(s *adminv1alpha1.WorkspaceSeedSpec) { s.Bindings[0].PermissionClaims[0].Selector = nil }, "must carry a selector"},
		"empty selector": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Bindings[0].PermissionClaims[0].Selector = &metav1.LabelSelector{}
		}, "must carry a selector"},
		"every configmap": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Bindings[0].PermissionClaims[0] = adminv1alpha1.SeedPermissionClaim{Resource: "configmaps", Verbs: []string{"get"}}
		}, "must carry a selector"},
		"bad selector": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Bindings[0].PermissionClaims[0].Selector.MatchExpressions[0].Operator = "Sometimes"
		}, "selector"},
		"manifest without a name": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Objects[0].Manifest.Raw = []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{}}`)
		}, "metadata.name"},
		"manifest not json": {func(s *adminv1alpha1.WorkspaceSeedSpec) { s.Objects[0].Manifest.Raw = []byte(`{`) }, "manifest"},
		"rbac": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Objects[0].Manifest.Raw = []byte(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRoleBinding","metadata":{"name":"x"}}`)
		}, "cannot be seeded"},
		"apibinding": {func(s *adminv1alpha1.WorkspaceSeedSpec) {
			s.Objects[0].Manifest.Raw = []byte(`{"apiVersion":"apis.kcp.io/v1alpha2","kind":"APIBinding","metadata":{"name":"x"}}`)
		}, "cannot be seeded"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			tc.mutate(&spec)
			if err := Validate(&spec); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	// A claim on something other than secrets and configmaps may cover
	// everything of the resource.
	spec := validSpec()
	spec.Bindings[0].PermissionClaims = append(spec.Bindings[0].PermissionClaims, adminv1alpha1.SeedPermissionClaim{Resource: "namespaces", Verbs: []string{"get"}})
	if err := Validate(&spec); err != nil {
		t.Fatalf("unscoped namespaces claim refused: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestStoreNotifiesOnlyOnChange(t *testing.T) {
	store := NewStore()
	calls := 0
	unregister := store.OnChange(func() { calls++ })

	store.Set("a", validSpec())
	store.Set("a", validSpec())
	if calls != 1 {
		t.Fatalf("calls after two equal sets = %d, want 1", calls)
	}
	changed := validSpec()
	changed.Bindings[0].TenantAccess = adminv1alpha1.SeedTenantAccessNone
	store.Set("a", changed)
	store.Set("b", validSpec())
	store.Delete("missing")
	store.Delete("b")
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
	seeds := store.List()
	if len(seeds) != 1 || seeds[0].Name != "a" || seeds[0].Spec.Bindings[0].TenantAccess != adminv1alpha1.SeedTenantAccessNone {
		t.Fatalf("seeds = %+v", seeds)
	}
	// What List returns is a copy.
	seeds[0].Spec.Bindings[0].Name = "mutated"
	if store.List()[0].Spec.Bindings[0].Name != "switchboard" {
		t.Fatal("List handed out the stored spec")
	}
	unregister()
	store.Delete("a")
	if calls != 4 {
		t.Fatalf("listener ran after unregister: %d", calls)
	}
}

type fakeOps struct {
	paths   map[string]string
	pathErr error
	results map[string]kcp.WorkspaceSeedResult
	errs    map[string]error
	ensured []string
}

func (f *fakeOps) ResolveClusterPath(_ context.Context, clusterName string) (string, error) {
	if f.pathErr != nil {
		return "", f.pathErr
	}
	return f.paths[clusterName], nil
}

func (f *fakeOps) EnsureWorkspaceSeed(_ context.Context, clusterName, seedName string, _ adminv1alpha1.WorkspaceSeedSpec) (kcp.WorkspaceSeedResult, error) {
	f.ensured = append(f.ensured, clusterName+"/"+seedName)
	return f.results[seedName], f.errs[seedName]
}

func request(cluster string) mcreconcile.Request {
	return mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: "agents"}},
		ClusterName: multicluster.ClusterName(cluster),
	}
}

func TestWorkspaceReconcile(t *testing.T) {
	ctx := context.Background()
	tenant := kcppaths.WorkspacePath("org-a", "ws-1")

	t.Run("no seeds, nothing resolved", func(t *testing.T) {
		ops := &fakeOps{pathErr: errors.New("must not be called")}
		r := &workspaceReconciler{ops: ops, store: NewStore(), paths: map[string]string{}}
		if res, err := r.Reconcile(ctx, request("c1")); err != nil || res.RequeueAfter != 0 {
			t.Fatalf("res %+v err %v", res, err)
		}
	})

	store := NewStore()
	store.Set("b-seed", validSpec())
	store.Set("a-seed", validSpec())

	t.Run("only tenant workspaces", func(t *testing.T) {
		for _, path := range []string{kcppaths.OrgPath("org-a"), kcppaths.ProviderPath("agents"), kcppaths.SystemProviders, tenant + ":edge-1"} {
			ops := &fakeOps{paths: map[string]string{"c1": path}}
			r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
			if _, err := r.Reconcile(ctx, request("c1")); err != nil {
				t.Fatal(err)
			}
			if len(ops.ensured) != 0 {
				t.Errorf("%s was seeded: %v", path, ops.ensured)
			}
		}
	})

	t.Run("every seed, in order, and settles", func(t *testing.T) {
		ops := &fakeOps{paths: map[string]string{"c1": tenant}}
		r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
		res, err := r.Reconcile(ctx, request("c1"))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("res %+v err %v", res, err)
		}
		if got := strings.Join(ops.ensured, ","); got != "c1/a-seed,c1/b-seed" {
			t.Fatalf("ensured = %s", got)
		}
	})

	t.Run("an unbound binding brings it back", func(t *testing.T) {
		ops := &fakeOps{paths: map[string]string{"c1": tenant}, results: map[string]kcp.WorkspaceSeedResult{"b-seed": {Unbound: 1, BindingsCreated: 1}}}
		r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
		res, err := r.Reconcile(ctx, request("c1"))
		if err != nil || res.RequeueAfter != unboundRequeue {
			t.Fatalf("res %+v err %v", res, err)
		}
	})

	t.Run("one failing seed does not stop the next", func(t *testing.T) {
		ops := &fakeOps{paths: map[string]string{"c1": tenant}, errs: map[string]error{"a-seed": errors.New("boom")}}
		r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
		_, err := r.Reconcile(ctx, request("c1"))
		if err == nil || !strings.Contains(err.Error(), `seed "a-seed": boom`) {
			t.Fatalf("err = %v", err)
		}
		if len(ops.ensured) != 2 {
			t.Fatalf("ensured = %v", ops.ensured)
		}
	})

	t.Run("a workspace that is gone", func(t *testing.T) {
		ops := &fakeOps{pathErr: apierrors.NewNotFound(schema.GroupResource{Group: "core.kcp.io", Resource: "logicalclusters"}, "cluster")}
		r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
		if _, err := r.Reconcile(ctx, request("c1")); err != nil {
			t.Fatal(err)
		}
	})
}

type clusterNames []multicluster.ClusterName

func (c clusterNames) ClusterNames() []multicluster.ClusterName { return c }

func TestStoreSourceFansOutToEveryEngagedWorkspace(t *testing.T) {
	store := NewStore()
	src := &storeSource{store: store, clusters: clusterNames{"c1", "c2"}}
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer q.ShutDown()
	ctx, cancel := context.WithCancel(context.Background())
	if err := src.Start(ctx, q); err != nil {
		t.Fatal(err)
	}
	store.Set("a", validSpec())
	if q.Len() != 2 {
		t.Fatalf("queued = %d, want one request per workspace", q.Len())
	}
	item, _ := q.Get()
	if item.Name != kcp.CoreBindingName {
		t.Fatalf("request = %+v", item)
	}
	q.Done(item)
	// The same change twice is one request per workspace, not two.
	changed := validSpec()
	changed.Objects = nil
	store.Set("a", changed)
	store.Set("b", validSpec())
	if q.Len() != 2 {
		t.Fatalf("queued = %d", q.Len())
	}
	cancel()
	before := q.Len()
	store.Delete("b")
	if q.Len() != before {
		t.Fatal("a stopped source still enqueues")
	}
}
