// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
)

func TestFirstCredentialWriteFailureRollback(t *testing.T) {
	for _, scenario := range []struct {
		name                                                             string
		activation, committed, edit, replace, newReservation, deleteRace bool
	}{
		{name: "Secret create rejected"},
		{name: "Secret create committed but response lost", committed: true},
		{name: "activation rejected", activation: true},
		{name: "activation committed but response lost", activation: true, committed: true},
		{name: "concurrent model edit", edit: true},
		{name: "same-name replacement", replace: true},
		{name: "concurrent key reservation", newReservation: true},
		{name: "change after rollback read", deleteRace: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, d := newCredentialDeleteClient(t)
			failure := errors.New("injected write failure")
			var parent *unstructured.Unstructured
			d.PrependReactor("create", "modelcredentials", func(a ktesting.Action) (bool, runtime.Object, error) {
				parent = a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				parent.SetUID("created-uid")
				parent.SetResourceVersion("1")
				return true, parent.DeepCopy(), d.Tracker().Create(agentsclient.ModelCredentialGVR, parent.DeepCopy(), "")
			})
			changeParent := func() {
				t.Helper()
				modified := parent.DeepCopy()
				modified.SetResourceVersion("2")
				if scenario.replace {
					modified.SetUID("replacement-uid")
				}
				if scenario.edit {
					if err := unstructured.SetNestedField(modified.Object, "changed-model", "spec", "model"); err != nil {
						t.Fatal(err)
					}
				}
				if scenario.newReservation {
					a := modified.GetAnnotations()
					a[agentsv1alpha1.PendingKeySecretAnnotation] = "another-key"
					modified.SetAnnotations(a)
				}
				if scenario.activation && scenario.committed {
					a := modified.GetAnnotations()
					delete(a, agentsv1alpha1.PendingKeySecretAnnotation)
					a[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = "activated"
					modified.SetAnnotations(a)
				}
				if err := d.Tracker().Update(agentsclient.ModelCredentialGVR, modified, ""); err != nil {
					t.Fatal(err)
				}
			}
			d.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
				key := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				if scenario.activation || scenario.committed {
					key.SetUID("key-uid")
					key.SetResourceVersion("1")
					if err := d.Tracker().Create(agentsclient.SecretGVR, key.DeepCopy(), "default"); err != nil {
						t.Fatal(err)
					}
				}
				if scenario.activation {
					return true, key, nil
				}
				if scenario.edit || scenario.replace || scenario.newReservation {
					changeParent()
				}
				return true, nil, failure
			})
			d.PrependReactor("patch", "modelcredentials", func(ktesting.Action) (bool, runtime.Object, error) {
				if scenario.committed {
					changeParent()
				}
				return true, nil, failure
			})
			d.PrependReactor("delete", "modelcredentials", func(a ktesting.Action) (bool, runtime.Object, error) {
				opts := a.(ktesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "created-uid" || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != "1" {
					t.Fatalf("unsafe rollback preconditions: %+v", opts.Preconditions)
				}
				if scenario.deleteRace {
					changeParent()
					return true, nil, apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), parent.GetName(), errors.New("concurrent edit"))
				}
				// Kubernetes retains the parent and its reservation until its Secret
				// protection finalizer finishes, including after a lost create response.
				current := parent.DeepCopy()
				ts := metav1.Now()
				current.SetDeletionTimestamp(&ts)
				return true, nil, d.Tracker().Update(agentsclient.ModelCredentialGVR, current, "")
			})
			_, err := applyCredentialUpsert(t.Context(), c, &modelCredential{Name: "new-model", Model: "one", APIKey: "test-key"})
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v, want original write failure", err)
			}
			if scenario.deleteRace && !apierrors.IsConflict(err) {
				t.Fatalf("rollback conflict missing: %v", err)
			}
			preserved := scenario.edit || scenario.replace || scenario.newReservation || (scenario.activation && scenario.committed)
			deletes := modelCredentialDeleteActions(d)
			if preserved && len(deletes) != 0 {
				t.Fatal("rollback deleted a changed or successfully activated credential")
			}
			if !preserved && len(deletes) != 1 {
				t.Fatalf("rollback DELETEs=%d, want 1", len(deletes))
			}
			current := getModelCredentialFromTracker(t, d, "new-model")
			if !preserved && !scenario.deleteRace && current.DeletionTimestamp.IsZero() {
				t.Fatal("incomplete first write was not submitted for finalizer cleanup")
			}
			for _, a := range d.Actions() {
				if a.GetResource() == agentsclient.SecretGVR && (a.GetVerb() == "delete" || a.GetVerb() == "update") {
					t.Fatal("API rollback directly changed Secret instead of using finalizer")
				}
			}
		})
	}
}

func TestRetryFirstCredentialWriteAfterCrashBeforeSecretCreation(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	missing := agentsv1alpha1.GeneratedModelCredentialSecretName(id)
	parent := directCredential("interrupted", "interrupted-uid", "1", missing)
	parent.Finalizers = []string{agentsv1alpha1.ModelCredentialSecretProtectionFinalizer}
	parent.Annotations = map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: missing}
	c, d := newCredentialDeleteClient(t, parent)
	d.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		key := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		key.SetUID("retried-key-uid")
		key.SetResourceVersion("1")
		return true, key, d.Tracker().Create(agentsclient.SecretGVR, key.DeepCopy(), "default")
	})
	if _, err := applyCredentialUpsert(t.Context(), c, &modelCredential{Name: parent.Name, Model: "repaired-model", APIKey: "retry-key"}); err != nil {
		t.Fatal(err)
	}
	current := getModelCredentialFromTracker(t, d, parent.Name)
	if current.UID != parent.UID || current.Spec.Model != "repaired-model" || current.Spec.SecretRef.Name == missing || current.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] != "" {
		t.Fatalf("retry failed to repair interrupted credential: %+v", current)
	}
	key := getSecretFromTracker(t, d, current.Spec.SecretRef.Name)
	if key == nil || string(key.Data[agentsv1alpha1.DefaultModelSecretKey]) != "retry-key" {
		t.Fatal("retry did not activate the supplied key")
	}
}

func TestFirstCredentialWriteAcknowledgesControllerActivation(t *testing.T) {
	for _, scenario := range []string{"before-writer-read", "between-read-and-patch", "lost-patch-response"} {
		t.Run(scenario, func(t *testing.T) {
			c, d := newCredentialDeleteClient(t)
			var parent *unstructured.Unstructured
			d.PrependReactor("create", "modelcredentials", func(a ktesting.Action) (bool, runtime.Object, error) {
				parent = a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				parent.SetUID("owner-uid")
				parent.SetResourceVersion("1")
				return true, parent.DeepCopy(), d.Tracker().Create(agentsclient.ModelCredentialGVR, parent.DeepCopy(), "")
			})
			activate := func() {
				t.Helper()
				active := parent.DeepCopy()
				annotations := active.GetAnnotations()
				pending := annotations[agentsv1alpha1.PendingKeySecretAnnotation]
				delete(annotations, agentsv1alpha1.PendingKeySecretAnnotation)
				annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = strings.TrimPrefix(pending, "railgrid-agents-model-")
				active.SetAnnotations(annotations)
				active.SetResourceVersion("2")
				if err := d.Tracker().Update(agentsclient.ModelCredentialGVR, active, ""); err != nil {
					t.Fatal(err)
				}
			}
			d.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
				key := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				key.SetUID("key-uid")
				key.SetResourceVersion("1")
				if err := d.Tracker().Create(agentsclient.SecretGVR, key.DeepCopy(), "default"); err != nil {
					t.Fatal(err)
				}
				if scenario == "before-writer-read" {
					activate()
				}
				return true, key, nil
			})
			d.PrependReactor("patch", "modelcredentials", func(ktesting.Action) (bool, runtime.Object, error) {
				activate()
				if scenario == "lost-patch-response" {
					return true, nil, errors.New("response lost after committed activation")
				}
				return true, nil, apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), parent.GetName(), errors.New("controller activated first"))
			})
			result, err := applyCredentialUpsert(t.Context(), c, &modelCredential{Name: "new-model", Model: "first-model", APIKey: "test-key"})
			if err != nil || result == nil || !result.HasAPIKey {
				t.Fatalf("successful activation reported as failure: result=%+v err=%v", result, err)
			}
			if len(modelCredentialDeleteActions(d)) != 0 {
				t.Fatal("acknowledged activation was rolled back")
			}
			creates := 0
			for _, a := range d.Actions() {
				if a.GetResource() == agentsclient.SecretGVR && a.GetVerb() == "create" {
					creates++
				}
			}
			if creates != 1 {
				t.Fatalf("created %d keys, want exactly one", creates)
			}
		})
	}
}
