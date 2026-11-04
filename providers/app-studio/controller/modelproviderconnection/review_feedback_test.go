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

package modelproviderconnection

import (
	"context"
	"strings"
	"testing"

	api "github.com/railgrid/provider-app-studio/api"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type feedbackCluster struct {
	cluster.Cluster
	client client.Client
}

func (c *feedbackCluster) GetClient() client.Client    { return c.client }
func (c *feedbackCluster) GetAPIReader() client.Reader { return c.client }

type feedbackManager struct {
	mcmanager.Manager
	cluster cluster.Cluster
}

func (m *feedbackManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return m.cluster, nil
}

func TestUnmigratableLegacyRowDoesNotStarveSibling(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-uid", "taken", "first-key", "https://api.openai.com/v1")
	sibling := studio.Spec.LLM.Models[0]
	sibling.ID = "second"
	sibling.RevisionID = "second-revision"
	sibling.SecretRef = &aiv1alpha1.StudioLLMSecretRef{Name: "second-key"}
	studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, sibling)
	independent := connectionForSnapshot("taken", "independent-uid", "https://independent.example/v1", "independent-key", "")
	c := newMigrationClient(t, studio, independent, legacyKeySecret("first-key", "first"), legacyKeySecret("second-key", "second"))
	for pass := 0; pass < 3; pass++ {
		err := (&Reconciler{}).migrateLegacyBindings(ctx, c, c)
		if err == nil || !strings.Contains(err.Error(), "already exists independently") {
			t.Fatalf("pass %d missing collision error: %v", pass, err)
		}
	}
	var migrated aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "second"}, &migrated); err != nil {
		t.Fatalf("healthy sibling was starved: %v", err)
	}
	if migrated.Spec.SecretRef.Name != "second-key" {
		t.Fatal("sibling migrated the wrong credential")
	}
	var original aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "taken"}, &original); err != nil {
		t.Fatal(err)
	}
	if original.UID != independent.UID || original.Spec.BaseURL != independent.Spec.BaseURL {
		t.Fatal("collision changed the independent connection")
	}
}

func TestExternalConnectionReconcilesAndDeletesWithoutStudio(t *testing.T) {
	ctx := context.Background()
	secret := legacyKeySecret("external-key", "synthetic-key")
	connection := connectionForSnapshot("external", "connection-uid", "https://provider.example/v1", secret.Name, "")
	connection.Generation = 1
	connection.Finalizers = []string{aiv1alpha1.ModelProviderConnectionFinalizer}
	c := newMigrationClient(t, connection, secret)
	r := &Reconciler{Manager: &feedbackManager{cluster: &feedbackCluster{client: c}}}
	request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}}}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatalf("missing Studio blocked reconcile: %v", err)
	}
	var observed aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, request.NamespacedName, &observed); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(observed.Status.Conditions, "SecretResolved") || observed.Status.ActiveSnapshot == nil {
		t.Fatal("missing Studio prevented credential status and admission")
	}
	if err := c.Delete(ctx, &observed); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatalf("missing Studio blocked deletion: %v", err)
	}
	if err := c.Get(ctx, request.NamespacedName, &observed); !apierrors.IsNotFound(err) {
		t.Fatalf("connection finalizer remained: %v", err)
	}
	var retained corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: "default"}, &retained); err != nil {
		t.Fatalf("external Secret was deleted: %v", err)
	}
}

func TestExternalSecretResourceVersionIsAnIntentionalRevisionBoundary(t *testing.T) {
	ctx := context.Background()
	secret := legacyKeySecret("external-key", "synthetic-key")
	connection := connectionForSnapshot("external", "connection-uid", "https://provider.example/v1", secret.Name, "")
	old, err := api.ModelProviderConnectionSnapshot(connection, secretObject(t, secret))
	if err != nil {
		t.Fatal(err)
	}
	changed := secret.DeepCopy()
	changed.ResourceVersion = "2"
	changed.Annotations = map[string]string{"owner-note": "metadata-only edit"}
	fresh, err := api.ModelProviderConnectionSnapshot(connection, secretObject(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	if old.ConnectionFingerprint == fresh.ConnectionFingerprint {
		t.Fatal("resourceVersion must invalidate an external-key fingerprint")
	}
	studio := legacyStudio("studio-uid", "model", secret.Name, connection.Spec.BaseURL)
	studio.Spec.LLM.Models[0].ConnectionRef = connection.Name
	studio.Spec.LLM.Models[0].ConnectionSnapshot = old
	c := newMigrationClient(t, studio, connection, changed)
	if err := (&Reconciler{}).rolloverBindings(ctx, c, c, connection, changed); err != nil {
		t.Fatal(err)
	}
	var observed aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &observed); err != nil {
		t.Fatal(err)
	}
	if len(observed.Spec.LLM.Models) != 2 || !observed.Spec.LLM.Models[0].Archived || observed.Spec.LLM.Models[1].ConnectionSnapshot.ConnectionFingerprint != fresh.ConnectionFingerprint {
		t.Fatal("external key version did not roll to a distinct pinned revision")
	}
}
