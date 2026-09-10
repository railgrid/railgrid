/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package packages

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

func TestDependencyEventsEnqueueOnlyTenantRepositories(t *testing.T) {
	t.Setenv("FAROS_TENANT_CREDENTIALS_NAMESPACE", "credentials")
	connection := func(name, namespace string) *codev1alpha1.Connection {
		return &codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: codev1alpha1.ConnectionSpec{SecretRef: codev1alpha1.LocalSecretReference{Name: "token", Namespace: namespace}}}
	}
	repository := func(name, conn string) *codev1alpha1.Repository {
		return &codev1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: codev1alpha1.RepositorySpec{ConnectionRef: conn}}
	}
	for _, tenantName := range []multicluster.ClusterName{"tenant-a", "tenant-b"} {
		t.Run(string(tenantName), func(t *testing.T) {
			// Same dependency names, distinct tenant caches; event context has no tenant.
			c := newFakeClient(connection("default-ns", ""), connection("explicit-ns", "credentials"), connection("other-ns", "other"),
				repository(string(tenantName)+"-one", "default-ns"), repository(string(tenantName)+"-two", "explicit-ns"), repository("unrelated", "other-ns"))
			for _, tc := range []struct {
				name   string
				secret bool
				obj    client.Object
				want   []string
			}{
				{"connection", false, connection("default-ns", ""), []string{string(tenantName) + "-one"}},
				{"secret", true, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "credentials"}}, []string{string(tenantName) + "-one", string(tenantName) + "-two"}},
				{"other namespace", true, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "other"}}, []string{"unrelated"}},
				{"missing", true, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "credentials"}}, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					h := dependencyHandler(tc.secret)(tenantName, pollingCluster{c: c})
					q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
					defer q.ShutDown()
					for _, kind := range []string{"create", "update", "delete"} {
						switch kind {
						case "create":
							h.Create(context.Background(), event.CreateEvent{Object: tc.obj}, q)
						case "update":
							h.Update(context.Background(), event.UpdateEvent{ObjectOld: tc.obj.DeepCopyObject().(client.Object), ObjectNew: tc.obj}, q)
						case "delete":
							h.Delete(context.Background(), event.DeleteEvent{Object: tc.obj}, q)
						}
						var got []string
						for q.Len() > 0 {
							req, _ := q.Get()
							if req.ClusterName != tenantName {
								t.Fatalf("cross-tenant request: %+v", req)
							}
							got = append(got, req.Name)
							q.Done(req)
						}
						sort.Strings(got)
						if !reflect.DeepEqual(got, tc.want) {
							t.Fatalf("%s: requests=%v want=%v", kind, got, tc.want)
						}
					}
				})
			}
		})
	}
}

func TestSucceededCommitEnqueuesItsRepository(t *testing.T) {
	commit := func(phase codev1alpha1.RepositoryCommitPhase) *codev1alpha1.RepositoryCommit {
		return &codev1alpha1.RepositoryCommit{
			ObjectMeta: metav1.ObjectMeta{Name: "demo-commit"},
			Spec:       codev1alpha1.RepositoryCommitSpec{RepositoryRef: "demo"},
			Status:     codev1alpha1.RepositoryCommitStatus{Phase: phase},
		}
	}
	running, succeeded := commit(codev1alpha1.RepositoryCommitPhaseRunning), commit(codev1alpha1.RepositoryCommitPhaseSucceeded)
	for _, tc := range []struct {
		name     string
		old, new *codev1alpha1.RepositoryCommit
		want     bool
	}{
		{"running to succeeded", running, succeeded, true},
		{"already succeeded", succeeded, succeeded, false},
		{"still running", running, running, false},
		{"failed", running, commit(codev1alpha1.RepositoryCommitPhaseFailed), false},
	} {
		if got := commitSucceeded.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new}); got != tc.want {
			t.Fatalf("%s: predicate = %v, want %v", tc.name, got, tc.want)
		}
	}
	if commitSucceeded.Create(event.CreateEvent{Object: succeeded}) || commitSucceeded.Delete(event.DeleteEvent{Object: succeeded}) {
		t.Fatal("create/delete events must not trigger a crawl")
	}

	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer q.ShutDown()
	commitHandler()("tenant-a", pollingCluster{c: newFakeClient()}).Update(context.Background(), event.UpdateEvent{ObjectOld: running, ObjectNew: succeeded}, q)
	if q.Len() != 1 {
		t.Fatalf("queued %d requests, want 1", q.Len())
	}
	req, _ := q.Get()
	defer q.Done(req)
	if req.ClusterName != "tenant-a" || req.Name != "demo" {
		t.Fatalf("request = %+v, want tenant-a/demo", req)
	}
}

func TestCredentialRotationWakesRepositoryBeforeOldReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		repo := testRepo()
		conn := &codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default"}}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("old")}}
		c := newFakeClient(repo, conn, secret)
		b := &pollingLister{err: &backend.RateLimitError{RetryAt: time.Now().Add(time.Hour)}}
		registry := backend.NewRegistry()
		if err := registry.Register(b); err != nil {
			t.Fatal(err)
		}
		r := &Reconciler{Manager: pollingManager{c: c}, Backends: registry, CrawlInterval: defaultCrawlInterval}
		req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}}
		result, err := r.Reconcile(ctx, req)
		if err != nil || result.RequeueAfter != time.Hour {
			t.Fatalf("initial throttle: %v %v", result, err)
		}
		q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
		defer q.ShutDown()
		q.AddAfter(req, result.RequeueAfter)
		synctest.Wait()
		if q.Len() != 0 {
			t.Fatal("delayed repository enqueued before reset")
		}
		old := secret.DeepCopy()
		secret.Data["token"] = []byte("rotated")
		if err := c.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
		dependencyHandler(true)(req.ClusterName, pollingCluster{c: c}).Update(ctx, event.UpdateEvent{ObjectOld: old, ObjectNew: secret}, q)
		if q.Len() != 1 {
			t.Fatal("credential rotation did not interrupt reset delay")
		}
		queued, _ := q.Get()
		defer q.Done(queued)
		b.err = nil
		if _, err := r.Reconcile(ctx, queued); err != nil {
			t.Fatal(err)
		}
		if b.token != "rotated" || b.calls != 2 {
			t.Fatalf("rotation not consumed: token=%q calls=%d", b.token, b.calls)
		}
	})
}
