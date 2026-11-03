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
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	api "github.com/railgrid/provider-app-studio/api"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func TestProbeResultRequiresExplicitResultForDesiredFingerprint(t *testing.T) {
	lastProbe := metav1.NewTime(time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC))
	status := &aiv1alpha1.ModelProviderConnectionStatus{
		ConnectionFingerprint: "desired-fingerprint",
		LastProbeFingerprint:  "desired-fingerprint",
		LastProbeTime:         &lastProbe,
	}
	if !probeResultMatches(status, "desired-fingerprint") {
		t.Fatal("matching explicitly-authorized probe result was not recognized")
	}
	if probeResultMatches(status, "changed-fingerprint") {
		t.Fatal("probe result for an older connection fingerprint was accepted")
	}
	status.LastProbeTime = nil
	if probeResultMatches(status, "desired-fingerprint") {
		t.Fatal("probe result without an authorization timestamp was accepted")
	}
}

func TestStudioStatusOnlyEventsAreFilteredFromConnectionWatch(t *testing.T) {
	filter := predicate.GenerationChangedPredicate{}
	oldStudio := &aiv1alpha1.Studio{ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, Generation: 4}}
	newStudio := oldStudio.DeepCopy()
	newStudio.Status.Phase = aiv1alpha1.StudioServiceReady
	if filter.Update(event.UpdateEvent{ObjectOld: oldStudio, ObjectNew: newStudio}) {
		t.Fatal("Studio status-only update should not enqueue every provider connection")
	}
	newStudio.Generation++
	if !filter.Update(event.UpdateEvent{ObjectOld: oldStudio, ObjectNew: newStudio}) {
		t.Fatal("Studio spec generation change should enqueue connections for migration or rollover")
	}
}

func TestWatchMapperScopeUsesOnlyTheSuppliedTenantObjects(t *testing.T) {
	studioA := legacyStudio("studio-a", "legacy-a", "key-a", "https://api.openai.com/v1")
	studioB := legacyStudio("studio-b", "legacy-b", "key-b", "https://api.openai.com/v1")
	connectionA := aiv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "shared-name"}}
	connectionB := aiv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "shared-name"}}
	gotA := WatchMapperScope(studioA, []aiv1alpha1.ModelProviderConnection{connectionA})
	gotB := WatchMapperScope(studioB, []aiv1alpha1.ModelProviderConnection{connectionB})
	if fmt.Sprint(gotA) != "[shared-name legacy-a]" || fmt.Sprint(gotB) != "[shared-name legacy-b]" {
		t.Fatalf("watch mappings crossed tenant inventories: A=%v B=%v", gotA, gotB)
	}
}

func TestStudioEventAlwaysEnqueuesCredentialCollectorWithoutConnections(t *testing.T) {
	requests := requestsForStudioEvent(nil, nil)
	if len(requests) != 1 || requests[0].Name != studioCredentialCollectorRequestName {
		t.Fatalf("Studio event with no connections mapped to %v, want the retained-key collector request", requests)
	}
}

func TestLegacyMigrationIsIdempotentAndTenantScoped(t *testing.T) {
	type tenantState struct {
		name   string
		uid    types.UID
		key    string
		studio *aiv1alpha1.Studio
		client client.Client
	}
	states := []tenantState{
		{name: "tenant-a", uid: "studio-a", key: "key-a"},
		{name: "tenant-b", uid: "studio-b", key: "key-b"},
	}
	for i := range states {
		state := &states[i]
		state.studio = legacyStudio(state.uid, "shared-model", "legacy-secret", "https://api.openai.com/v1")
		second := aiv1alpha1.StudioLLMModel{
			ID: "second-model", RevisionID: "legacy-revision-second", Name: "Second legacy", Provider: "openai-compatible",
			BaseURL: "https://api.openai.com/v1", Model: "gpt-5-mini", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: "legacy-secret-second"},
		}
		state.studio.Spec.LLM.Models = append(state.studio.Spec.LLM.Models, second)
		state.client = newMigrationClient(t, state.studio,
			legacyKeySecret("legacy-secret", state.key), legacyKeySecret("legacy-secret-second", state.key+"-second"))
	}

	for pass := 0; pass < 4; pass++ {
		for i := range states {
			state := &states[i]
			r := &Reconciler{}
			if err := r.migrateLegacyBindings(context.Background(), state.client, state.client); err != nil {
				t.Fatalf("%s migration pass %d: %v", state.name, pass+1, err)
			}
		}
	}

	for i := range states {
		state := &states[i]
		var studio aiv1alpha1.Studio
		if err := state.client.Get(context.Background(), types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
			t.Fatalf("%s get migrated Studio: %v", state.name, err)
		}
		if len(studio.Spec.LLM.Models) != 4 {
			t.Fatalf("%s Studio has %d model revisions, want two archived and two active", state.name, len(studio.Spec.LLM.Models))
		}
		active := map[string]aiv1alpha1.StudioLLMModel{}
		archived := map[string]aiv1alpha1.StudioLLMModel{}
		for _, model := range studio.Spec.LLM.Models {
			if model.Archived {
				archived[model.RevisionID] = model
			} else {
				active[model.ID] = model
			}
		}
		for _, want := range []struct{ id, revision, endpoint, secret string }{
			{"shared-model", "legacy-revision", "https://api.openai.com/v1", "legacy-secret"},
			{"second-model", "legacy-revision-second", "https://api.openai.com/v1", "legacy-secret-second"},
		} {
			old, ok := archived[want.revision]
			if !ok || old.ID != want.id {
				t.Fatalf("%s legacy revision %q was not preserved as archived: %#v", state.name, want.revision, old)
			}
			current, ok := active[want.id]
			if !ok || current.ConnectionRef != want.id || current.ConnectionSnapshot == nil || current.ConnectionSnapshot.ConnectionUID == "" {
				t.Fatalf("%s migrated model %q = %#v, want active connection snapshot", state.name, want.id, current)
			}
			if current.ConnectionSnapshot.BaseURL != want.endpoint || current.ConnectionSnapshot.SecretRef == nil || current.ConnectionSnapshot.SecretRef.Name != want.secret {
				t.Fatalf("%s migration changed model %q endpoint or Secret: %#v", state.name, want.id, current.ConnectionSnapshot)
			}
		}
		var connections aiv1alpha1.ModelProviderConnectionList
		if err := state.client.List(context.Background(), &connections); err != nil {
			t.Fatalf("%s list migrated connections: %v", state.name, err)
		}
		if len(connections.Items) != 2 {
			t.Fatalf("%s has %d connections for two distinct legacy model Secrets sharing one URL, want two", state.name, len(connections.Items))
		}
		for _, connection := range connections.Items {
			if connection.UID == "" {
				t.Fatalf("%s connection %q has no persisted UID", state.name, connection.Name)
			}
		}
	}
	if states[0].studio.UID == states[1].studio.UID {
		t.Fatal("test fixtures must model distinct tenant Studios")
	}
}

func TestExistingMigrationConnectionResumesAfterStudioUpdateConflict(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-migration-retry", "fast", "legacy-key", "https://api.openai.com/v1")
	base := fake.NewClientBuilder().WithScheme(controllerScheme(t)).WithObjects(studio, legacyKeySecret("legacy-key", "saved-key")).
		WithStatusSubresource(&aiv1alpha1.Studio{}, &aiv1alpha1.ModelProviderConnection{}).Build()
	writer := &conflictOnceStudioUpdateClient{migrationClient: &migrationClient{Client: base}, fail: true}
	r := &Reconciler{}
	if err := r.migrateLegacyBindings(ctx, writer, base); !apierrors.IsConflict(err) {
		t.Fatalf("first migration result = %v, want injected Studio conflict", err)
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := base.Get(ctx, types.NamespacedName{Name: "fast"}, &connection); err != nil {
		t.Fatalf("connection create was not retained after conflict: %v", err)
	}
	resumed, err := r.resumeLegacyMigration(ctx, writer, base, &connection)
	if err != nil || !resumed {
		t.Fatalf("existing connection migration resumed=%t err=%v", resumed, err)
	}
	var migrated aiv1alpha1.Studio
	if err := base.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &migrated); err != nil {
		t.Fatal(err)
	}
	if len(migrated.Spec.LLM.Models) != 2 || !migrated.Spec.LLM.Models[0].Archived || migrated.Spec.LLM.Models[1].ConnectionRef != "fast" {
		t.Fatalf("Studio after migration resume = %#v", migrated.Spec.LLM.Models)
	}
}

func TestLegacyMigrationNormalizesProviderDefaultAndResumesAfterConflict(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		baseURL  string
	}{
		{name: "openai-compatible", provider: "openai-compatible", baseURL: "https://api.openai.com/v1"},
		{name: "google", provider: "google-ai-studio", baseURL: "https://generativelanguage.googleapis.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			studio := legacyStudio(types.UID("studio-default-"+test.name), "legacy-model", "legacy-key", "")
			studio.Spec.LLM.Models[0].Provider = test.provider
			base := newMigrationClient(t, studio, legacyKeySecret("legacy-key", "credential"))
			writer := &conflictOnceStudioUpdateClient{migrationClient: base.(*migrationClient), fail: true}
			r := &Reconciler{}
			if err := r.migrateLegacyBindings(ctx, writer, base); !apierrors.IsConflict(err) {
				t.Fatalf("first migration result = %v, want injected Studio conflict", err)
			}
			var connection aiv1alpha1.ModelProviderConnection
			if err := base.Get(ctx, types.NamespacedName{Name: "legacy-model"}, &connection); err != nil {
				t.Fatalf("normalized destination connection was not created: %v", err)
			}
			if connection.Spec.BaseURL != test.baseURL {
				t.Fatalf("migrated endpoint = %q, want provider default %q", connection.Spec.BaseURL, test.baseURL)
			}
			resumed, err := r.resumeLegacyMigration(ctx, writer, base, &connection)
			if err != nil || !resumed {
				t.Fatalf("migration retry resumed=%t err=%v", resumed, err)
			}
			var migrated aiv1alpha1.Studio
			if err := base.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &migrated); err != nil {
				t.Fatal(err)
			}
			if len(migrated.Status.LegacyModelMigrations) != 1 || migrated.Status.LegacyModelMigrations[0].Model.BaseURL != "" {
				t.Fatalf("legacy provenance changed during endpoint normalization: %#v", migrated.Status.LegacyModelMigrations)
			}
			if len(migrated.Spec.LLM.Models) != 2 || !migrated.Spec.LLM.Models[0].Archived || migrated.Spec.LLM.Models[0].BaseURL != "" {
				t.Fatalf("original legacy revision did not preserve omitted endpoint: %#v", migrated.Spec.LLM.Models)
			}
			active := migrated.Spec.LLM.Models[1]
			if active.ConnectionRef != "legacy-model" || active.ConnectionSnapshot == nil || active.ConnectionSnapshot.BaseURL != test.baseURL {
				t.Fatalf("active replacement snapshot = %#v, want normalized endpoint %q", active.ConnectionSnapshot, test.baseURL)
			}
		})
	}
}

func TestFinalizerRetainsAllGeneratedKeysAcrossConcurrentStudioBinding(t *testing.T) {
	ctx := context.Background()
	id := "0123456789abcdef0123456789abcdef"
	connection := connectionForSnapshot("primary", "connection-finalizing", "https://api.example/v1", aiv1alpha1.ModelProviderConnectionSecretPrefix+id, id)
	connection.Finalizers = []string{aiv1alpha1.ModelProviderConnectionFinalizer}
	secret := generatedSecret(id, "key", connection)
	studio := &aiv1alpha1.Studio{
		ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, UID: "studio-uid"},
		Spec:       aiv1alpha1.StudioSpec{LLM: &aiv1alpha1.StudioLLM{}},
	}
	c := newMigrationClient(t, studio, connection, secret)
	reader := &injectStudioBindingOnSecretListReader{Reader: c, writer: c, connectionName: connection.Name, secretName: secret.Name}
	var deleting aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: connection.Name}, &deleting); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Reconciler{}).finalize(ctx, c, reader, &deleting); err != nil {
		t.Fatalf("finalize connection after concurrent binding: %v", err)
	}
	if !reader.injected {
		t.Fatal("test did not inject a binding between the Studio read and key retention")
	}
	var currentStudio aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &currentStudio); err != nil {
		t.Fatal(err)
	}
	if len(currentStudio.Spec.LLM.Models) != 1 || currentStudio.Spec.LLM.Models[0].ConnectionRef != connection.Name {
		t.Fatalf("concurrent Studio binding was lost: %#v", currentStudio.Spec.LLM.Models)
	}
	var currentSecret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: secret.Name}, &currentSecret); err != nil {
		t.Fatalf("generated key was lost during finalization: %v", err)
	}
	if !secretOwnedByStudio(&currentSecret, &currentStudio) {
		t.Fatalf("concurrent binding key lacks Studio retention owner: %v", currentSecret.OwnerReferences)
	}
	var finalized aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: connection.Name}, &finalized); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(finalized.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer) {
		t.Fatal("connection finalizer did not complete after all generated keys were retained")
	}
}

func TestGeneratedKeyCollectionRechecksSnapshotAfterStudioStatusCASConflict(t *testing.T) {
	ctx := context.Background()
	id := "0123456789abcdef0123456789abcdef"
	secret := generatedSecret(id, "key", connectionForSnapshot("deleted-source", "source-uid", "https://api.example/v1", aiv1alpha1.ModelProviderConnectionSecretPrefix+id, id))
	secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Studio", Name: aiv1alpha1.StudioName, UID: "studio-uid"}}
	secret.Annotations[retainedConnectionUIDAnnotation] = "source-uid"
	studio := &aiv1alpha1.Studio{
		ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, UID: "studio-uid"},
		Spec:       aiv1alpha1.StudioSpec{LLM: &aiv1alpha1.StudioLLM{}},
	}
	base := newMigrationClient(t, studio, secret)
	writer := &injectStudioSnapshotBeforeStatusClient{Client: base, base: base, secret: secret, connectionName: "deleted-source", generatedID: id}
	err := (&Reconciler{}).collectStudioOwnedGeneratedCredentials(ctx, writer, base)
	if !apierrors.IsConflict(err) {
		t.Fatalf("collection status update = %v, want resourceVersion conflict", err)
	}
	var currentStudio aiv1alpha1.Studio
	if err := base.Get(ctx, types.NamespacedName{Name: studio.Name}, &currentStudio); err != nil {
		t.Fatal(err)
	}
	if len(currentStudio.Spec.LLM.Models) != 1 || len(currentStudio.Status.RetiredModelCredentials) != 0 {
		t.Fatalf("concurrent snapshot/tombstone state = models %#v, retired %#v", currentStudio.Spec.LLM.Models, currentStudio.Status.RetiredModelCredentials)
	}
	var retained corev1.Secret
	if err := base.Get(ctx, types.NamespacedName{Namespace: "default", Name: secret.Name}, &retained); err != nil {
		t.Fatalf("Secret was deleted despite a failed Studio CAS: %v", err)
	}
	if err := (&Reconciler{}).collectStudioOwnedGeneratedCredentials(ctx, base, base); err != nil {
		t.Fatalf("retry collection after concurrent snapshot: %v", err)
	}
	if err := base.Get(ctx, types.NamespacedName{Namespace: "default", Name: secret.Name}, &retained); err != nil {
		t.Fatalf("referenced Secret was deleted on retry: %v", err)
	}
}

func TestLegacyMigrationResumesWhenSourceSecretArrives(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-late-secret", "legacy-model", "late-legacy-key", "https://api.openai.com/v1")
	c := newMigrationClient(t, studio)
	r := &Reconciler{}
	if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
		t.Fatalf("initial migration with missing Secret: %v", err)
	}
	var connections aiv1alpha1.ModelProviderConnectionList
	if err := c.List(ctx, &connections); err != nil {
		t.Fatal(err)
	}
	if len(connections.Items) != 0 {
		t.Fatalf("missing source Secret created %d destination connections", len(connections.Items))
	}

	secret := legacyKeySecret("late-legacy-key", "arrived-key")
	secret.ResourceVersion = ""
	if err := c.Create(ctx, secret); err != nil {
		t.Fatalf("create source Secret: %v", err)
	}
	var currentStudio aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &currentStudio); err != nil {
		t.Fatal(err)
	}
	requests := requestsForSecretEvent(secret, &currentStudio, nil)
	if len(requests) != 1 || requests[0].Name != "legacy-model" {
		t.Fatalf("late legacy Secret watch requests = %v, want [legacy-model]", requests)
	}
	if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
		t.Fatalf("migration after Secret arrival: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &currentStudio); err != nil {
		t.Fatal(err)
	}
	if len(currentStudio.Spec.LLM.Models) != 2 || currentStudio.Spec.LLM.Models[1].ConnectionRef != "legacy-model" {
		t.Fatalf("Studio after late Secret migration = %#v", currentStudio.Spec.LLM.Models)
	}
}

func TestExistingMigrationConnectionParksUntilSourceSecretArrives(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-late-source", "fast", "late-source-key", "https://api.openai.com/v1")
	connection := &aiv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fast", UID: "migration-destination-uid",
			Annotations: map[string]string{
				migratedFromStudioUIDAnnotation:      string(studio.UID),
				migratedFromStudioRevisionAnnotation: studio.Spec.LLM.Models[0].RevisionID,
			},
		},
		Spec: aiv1alpha1.ModelProviderConnectionSpec{
			Provider: "openai-compatible", BaseURL: "https://api.openai.com/v1",
			SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: "late-source-key"}, SecretKey: aiv1alpha1.ModelProviderConnectionSecretKey,
		},
	}
	c := newMigrationClient(t, studio, connection)
	r := &Reconciler{}
	var stored aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "fast"}, &stored); err != nil {
		t.Fatal(err)
	}
	resumed, err := r.resumeLegacyMigration(ctx, c, c, &stored)
	if err != nil || resumed {
		t.Fatalf("migration with missing source Secret resumed=%t err=%v, want parked without requeue", resumed, err)
	}

	secret := legacyKeySecret("late-source-key", "arrived-key")
	secret.ResourceVersion = ""
	if err := c.Create(ctx, secret); err != nil {
		t.Fatalf("create source Secret: %v", err)
	}
	var connections aiv1alpha1.ModelProviderConnectionList
	if err := c.List(ctx, &connections); err != nil {
		t.Fatal(err)
	}
	requests := requestsForSecretEvent(secret, studio, connections.Items)
	if len(requests) != 1 || requests[0].Name != "fast" {
		t.Fatalf("late source Secret watch requests = %v, want [fast]", requests)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "fast"}, &stored); err != nil {
		t.Fatal(err)
	}
	resumed, err = r.resumeLegacyMigration(ctx, c, c, &stored)
	if err != nil || !resumed {
		t.Fatalf("migration after source Secret arrival resumed=%t err=%v", resumed, err)
	}
}

func TestConnectionRotationSnapshotsNewRevisionAndRetainsArchivedKeyOnDelete(t *testing.T) {
	ctx := context.Background()
	oldID := "0123456789abcdef0123456789abcdef"
	newID := "abcdef0123456789abcdef0123456789"
	connectionUID := types.UID("connection-uid")
	connection := &aiv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary", UID: connectionUID,
			Annotations: map[string]string{
				aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: newID,
			},
			Finalizers: []string{aiv1alpha1.ModelProviderConnectionFinalizer},
		},
		Spec: aiv1alpha1.ModelProviderConnectionSpec{
			Provider: "openai-compatible", BaseURL: "https://api.openai.com/v1",
			SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: aiv1alpha1.ModelProviderConnectionSecretPrefix + newID},
		},
	}
	oldSecret := generatedSecret(oldID, "old-key", connection)
	newSecret := generatedSecret(newID, "new-key", connection)
	oldSnapshot, err := api.ModelProviderConnectionSnapshot(connectionForSnapshot("primary", connectionUID, "https://old.example/v1", oldSecret.Name, oldID), secretObject(t, oldSecret))
	if err != nil {
		t.Fatalf("old snapshot: %v", err)
	}
	studio := &aiv1alpha1.Studio{
		ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, UID: "studio-uid"},
		Spec: aiv1alpha1.StudioSpec{LLM: &aiv1alpha1.StudioLLM{
			DefaultModel: "fast",
			Models: []aiv1alpha1.StudioLLMModel{{
				ID: "fast", RevisionID: "revision-before-rotation", Name: "Fast", Model: "gpt-5",
				ReasoningEffort: "high", ConnectionRef: "primary", ConnectionSnapshot: oldSnapshot,
			}},
		}},
	}
	scheme := controllerScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(studio, connection, oldSecret, newSecret).
		WithStatusSubresource(&aiv1alpha1.Studio{}, &aiv1alpha1.ModelProviderConnection{}).Build()
	r := &Reconciler{}
	if err := r.rolloverBindings(ctx, c, c, connection, newSecret); err != nil {
		t.Fatalf("roll over binding: %v", err)
	}
	var rotated aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &rotated); err != nil {
		t.Fatal(err)
	}
	if len(rotated.Spec.LLM.Models) != 2 {
		t.Fatalf("rotation created %d revisions, want 2", len(rotated.Spec.LLM.Models))
	}
	archived, active := rotated.Spec.LLM.Models[0], rotated.Spec.LLM.Models[1]
	if !archived.Archived || archived.RevisionID != "revision-before-rotation" || archived.ConnectionSnapshot.SecretRef.Name != oldSecret.Name {
		t.Fatalf("old model revision lost its pinned snapshot: %#v", archived)
	}
	if active.Archived || active.RevisionID == archived.RevisionID || active.ConnectionSnapshot == nil || active.ConnectionSnapshot.SecretRef.Name != newSecret.Name {
		t.Fatalf("new revision does not pin the rotated credential: %#v", active)
	}
	if active.Model != archived.Model || active.ReasoningEffort != archived.ReasoningEffort {
		t.Fatalf("rotation changed model settings: old=%#v new=%#v", archived, active)
	}

	// Removing the logical model leaves only its archived revision. Finalization
	// transfers every generated key to the Studio before releasing the source.
	rotated.Spec.LLM.Models = []aiv1alpha1.StudioLLMModel{archived}
	if err := c.Update(ctx, &rotated); err != nil {
		t.Fatalf("remove active model binding: %v", err)
	}
	if err := retainAllGeneratedSecrets(ctx, c, c, connection, &rotated); err != nil {
		t.Fatalf("retain generated keys while source connection is live: %v", err)
	}
	if err := r.collectStudioOwnedGeneratedCredentials(ctx, c, c); err != nil {
		t.Fatalf("collect while source connection is live: %v", err)
	}
	var currentKey corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: newSecret.Name}, &currentKey); err != nil {
		t.Fatalf("key was collected while source connection remained live: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: connection.Name}, connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, connection); err != nil {
		t.Fatalf("delete connection: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: connection.Name}, connection); err != nil {
		t.Fatalf("get deleting connection: %v", err)
	}
	if connection.DeletionTimestamp.IsZero() {
		t.Fatal("connection with a finalizer did not enter deletion")
	}
	if _, err := r.finalize(ctx, c, c, connection); err != nil {
		t.Fatalf("finalize connection: %v", err)
	}
	var retained corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: oldSecret.Name}, &retained); err != nil {
		t.Fatalf("archived key Secret was deleted: %v", err)
	}
	if string(retained.Data[aiv1alpha1.ModelProviderConnectionSecretKey]) != "old-key" || !secretOwnedByStudio(&retained, &rotated) {
		t.Fatalf("archived key after connection deletion = %#v, want old key owned by Studio", retained)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: newSecret.Name}, &currentKey); err != nil {
		t.Fatalf("unreferenced generated key was not retained through finalizer release: %v", err)
	}
	if !secretOwnedByStudio(&currentKey, &rotated) || currentKey.Annotations[retainedConnectionUIDAnnotation] != string(connectionUID) {
		t.Fatalf("unreferenced generated key was not transferred with source identity: owners=%v annotations=%v", currentKey.OwnerReferences, currentKey.Annotations)
	}
	var finalConnection aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: connection.Name}, &finalConnection); err == nil {
		if slices.Contains(finalConnection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer) {
			t.Fatal("connection finalizer was not released")
		}
		if err := c.Delete(ctx, &finalConnection); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("remove finalized connection: %v", err)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err := r.collectStudioOwnedGeneratedCredentials(ctx, c, c); err != nil {
		t.Fatalf("collect unreferenced generated key after source deletion: %v", err)
	}
	var deleted corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: newSecret.Name}, &deleted); !apierrors.IsNotFound(err) {
		t.Fatalf("unreferenced rotated key still exists after source deletion (err=%v)", err)
	}
	var currentStudio aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &currentStudio); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(currentStudio.Status.RetiredModelCredentials, aiv1alpha1.StudioModelCredentialIdentity{SecretName: newSecret.Name, SecretUID: string(newSecret.UID)}) {
		t.Fatalf("deleted key identity was not tombstoned first: %#v", currentStudio.Status.RetiredModelCredentials)
	}
	currentStudio.Spec.LLM.Models = nil
	if err := c.Update(ctx, &currentStudio); err != nil {
		t.Fatalf("remove last archived reference: %v", err)
	}
	if err := r.collectStudioOwnedGeneratedCredentials(ctx, c, c); err != nil {
		t.Fatalf("collect key after archived revision removal: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: oldSecret.Name}, &deleted); !apierrors.IsNotFound(err) {
		t.Fatalf("archived generated key still exists after revision removal (err=%v)", err)
	}
}

func legacyStudio(uid types.UID, modelID, secretName, baseURL string) *aiv1alpha1.Studio {
	return &aiv1alpha1.Studio{
		ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, UID: uid},
		Spec: aiv1alpha1.StudioSpec{LLM: &aiv1alpha1.StudioLLM{
			DefaultModel: modelID,
			Models: []aiv1alpha1.StudioLLMModel{{
				ID: modelID, RevisionID: "legacy-revision", Name: "Legacy", Provider: "openai-compatible",
				BaseURL: baseURL, Model: "gpt-5", ReasoningEffort: "high",
				SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: secretName},
			}},
		}},
	}
}

func legacyKeySecret(name, key string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("secret-" + key), ResourceVersion: "1"},
		Data:       map[string][]byte{aiv1alpha1.ModelProviderConnectionSecretKey: []byte(key)},
	}
}

type migrationClient struct {
	client.Client
	nextUID int
}

type conflictOnceStudioUpdateClient struct {
	*migrationClient
	fail bool
}

type injectStudioBindingOnSecretListReader struct {
	client.Reader
	writer         client.Client
	connectionName string
	secretName     string
	injected       bool
}

type injectStudioSnapshotBeforeStatusClient struct {
	client.Client
	base           client.Client
	secret         *corev1.Secret
	connectionName string
	generatedID    string
	injected       bool
}

func (c *injectStudioSnapshotBeforeStatusClient) Status() client.SubResourceWriter {
	return &injectStudioSnapshotBeforeStatusWriter{SubResourceWriter: c.Client.Status(), owner: c}
}

type injectStudioSnapshotBeforeStatusWriter struct {
	client.SubResourceWriter
	owner *injectStudioSnapshotBeforeStatusClient
}

func (w *injectStudioSnapshotBeforeStatusWriter) Update(ctx context.Context, object client.Object, opts ...client.SubResourceUpdateOption) error {
	if _, ok := object.(*aiv1alpha1.Studio); ok && !w.owner.injected {
		var studio aiv1alpha1.Studio
		if err := w.owner.base.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
			return err
		}
		studio.Spec.LLM.Models = []aiv1alpha1.StudioLLMModel{{
			ID: "racing-model", RevisionID: "racing-revision", Name: "Racing", Model: "gpt-5", ConnectionRef: w.owner.connectionName,
			ConnectionSnapshot: &aiv1alpha1.StudioLLMConnectionSnapshot{
				ConnectionRef: w.owner.connectionName, ConnectionUID: "source-uid", GeneratedSecretID: w.owner.generatedID,
				SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: w.owner.secret.Name}, SecretUID: string(w.owner.secret.UID),
				SecretResourceVersion: w.owner.secret.ResourceVersion, ConnectionFingerprint: "fingerprint",
			},
		}}
		if err := w.owner.base.Update(ctx, &studio); err != nil {
			return err
		}
		w.owner.injected = true
	}
	return w.SubResourceWriter.Update(ctx, object, opts...)
}

func (r *injectStudioBindingOnSecretListReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.SecretList); ok && !r.injected {
		var studio aiv1alpha1.Studio
		if err := r.writer.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
			return err
		}
		studio.Spec.LLM.Models = []aiv1alpha1.StudioLLMModel{{
			ID: "concurrent-model", RevisionID: "concurrent-r1", Name: "Concurrent", Model: "gpt-5",
			ConnectionRef: r.connectionName,
			ConnectionSnapshot: &aiv1alpha1.StudioLLMConnectionSnapshot{
				ConnectionRef: r.connectionName, SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: r.secretName},
			},
		}}
		if err := r.writer.Update(ctx, &studio); err != nil {
			return err
		}
		r.injected = true
	}
	return r.Reader.List(ctx, list, opts...)
}

func (c *conflictOnceStudioUpdateClient) Update(ctx context.Context, object client.Object, opts ...client.UpdateOption) error {
	if c.fail {
		if studio, ok := object.(*aiv1alpha1.Studio); ok {
			c.fail = false
			return apierrors.NewConflict(schema.GroupResource{Group: aiv1alpha1.GroupName, Resource: "studios"}, studio.Name, errors.New("injected conflict"))
		}
	}
	return c.migrationClient.Update(ctx, object, opts...)
}

func (c *migrationClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if object.GetUID() == "" {
		c.nextUID++
		object.SetUID(types.UID(fmt.Sprintf("created-uid-%d", c.nextUID)))
	}
	return c.Client.Create(ctx, object, opts...)
}

func newMigrationClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	base := fake.NewClientBuilder().WithScheme(controllerScheme(t)).WithObjects(objects...).
		WithStatusSubresource(&aiv1alpha1.Studio{}, &aiv1alpha1.ModelProviderConnection{}).Build()
	return &migrationClient{Client: base}
}

func controllerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func generatedSecret(id, key string, connection *aiv1alpha1.ModelProviderConnection) *corev1.Secret {
	immutable := true
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: aiv1alpha1.ModelProviderConnectionSecretPrefix + id, Namespace: "default",
			UID: types.UID("secret-" + id), ResourceVersion: "1",
			Labels:      map[string]string{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
			Annotations: map[string]string{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: id},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection",
				Name: connection.Name, UID: connection.UID,
			}},
		},
		Immutable: &immutable,
		Data:      map[string][]byte{aiv1alpha1.ModelProviderConnectionSecretKey: []byte(key)},
	}
}

func connectionForSnapshot(name string, uid types.UID, baseURL, secretName, generatedID string) *aiv1alpha1.ModelProviderConnection {
	return &aiv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, UID: uid,
			Annotations: map[string]string{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: generatedID},
		},
		Spec: aiv1alpha1.ModelProviderConnectionSpec{
			Provider: "openai-compatible", BaseURL: baseURL,
			SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: secretName},
		},
	}
}

func secretObject(t *testing.T, secret *corev1.Secret) *unstructured.Unstructured {
	t.Helper()
	value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secret)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: value}
}

func TestLegacyMigrationSkipsMissingAndEmptyKeys(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			ctx := context.Background()
			studio := legacyStudio("studio-uid", "unavailable", "missing-key", "https://api.openai.com/v1")
			studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, aiv1alpha1.StudioLLMModel{
				ID: "valid", RevisionID: "valid-revision", Name: "Valid", Model: "gpt-4o", Provider: "openai-compatible",
				BaseURL: "https://api.openai.com/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: "valid-key"},
			})
			objects := []client.Object{studio, legacyKeySecret("valid-key", "credential")}
			if empty {
				objects = append(objects, legacyKeySecret("missing-key", ""))
			}
			c := newMigrationClient(t, objects...)
			r := &Reconciler{}
			if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
				t.Fatal(err)
			}
			var connection aiv1alpha1.ModelProviderConnection
			if err := c.Get(ctx, types.NamespacedName{Name: "valid"}, &connection); err != nil {
				t.Fatalf("valid later model did not migrate: %v", err)
			}
			if err := c.Get(ctx, types.NamespacedName{Name: "unavailable"}, &connection); !apierrors.IsNotFound(err) {
				t.Fatalf("unavailable key should remain parked: %v", err)
			}
		})
	}
}

func TestLegacyMigrationDoesNotLaunderSavedConnectionKeys(t *testing.T) {
	ctx := context.Background()
	for _, generated := range []bool{false, true} {
		t.Run(fmt.Sprintf("generated=%t", generated), func(t *testing.T) {
			secretName := "external-key"
			if generated {
				secretName = aiv1alpha1.ModelProviderConnectionSecretPrefix + "0123456789abcdef0123456789abcdef"
			}
			studio := legacyStudio("studio-uid", "forged", secretName, "https://attacker.example/v1")
			connection := connectionForSnapshot("restricted", "connection-uid", "https://api.openai.com/v1", secretName, "")
			if !generated {
				connection.Spec.SecretRef.Name = " " + secretName + " "
			}
			c := newMigrationClient(t, studio, legacyKeySecret(secretName, "credential"), connection)
			r := &Reconciler{}
			if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
				t.Fatal(err)
			}
			var forged aiv1alpha1.ModelProviderConnection
			if err := c.Get(ctx, types.NamespacedName{Name: "forged"}, &forged); !apierrors.IsNotFound(err) {
				t.Fatalf("forged binding created a new connection: %v", err)
			}
		})
	}
}

func TestLegacyMigrationDoesNotLaunderActiveExternalKeyDuringRotation(t *testing.T) {
	ctx := context.Background()
	for _, generated := range []bool{false} {
		t.Run(fmt.Sprintf("generated=%t", generated), func(t *testing.T) {
			secretName := "external-key"
			if generated {
				secretName = aiv1alpha1.ModelProviderConnectionSecretPrefix + "0123456789abcdef0123456789abcdef"
			}
			studio := legacyStudio("studio-uid", "forged", secretName, "https://attacker.example/v1")
			connection := connectionForSnapshot("restricted", "connection-uid", "https://api.openai.com/v1", secretName, "")
			if !generated {
				connection.Spec.SecretRef.Name = " " + secretName + " "
			}
			connection.Status.ActiveSnapshot = &aiv1alpha1.StudioLLMConnectionSnapshot{SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: secretName}}
			connection.Spec.SecretRef.Name = "replacement-key"
			c := newMigrationClient(t, studio, legacyKeySecret(secretName, "credential"), connection)
			r := &Reconciler{}
			if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
				t.Fatal(err)
			}
			var observed aiv1alpha1.Studio
			if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &observed); err != nil {
				t.Fatal(err)
			}
			if len(observed.Status.LegacyModelMigrations) != 0 {
				t.Fatal("active external key was captured as legacy provenance")
			}
			var forged aiv1alpha1.ModelProviderConnection
			if err := c.Get(ctx, types.NamespacedName{Name: "forged"}, &forged); !apierrors.IsNotFound(err) {
				t.Fatalf("forged binding created a new connection: %v", err)
			}
		})
	}
}

func TestLegacyMigrationAtCapacityUsesRecordedSourceWithoutGrowingHistory(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-uid", "full", "legacy-key", "https://api.openai.com/v1")
	for i := 1; i < 200; i++ {
		archived := studio.Spec.LLM.Models[0]
		archived.RevisionID = fmt.Sprintf("archived-%d", i)
		archived.Archived = true
		studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, archived)
	}
	c := newMigrationClient(t, studio, legacyKeySecret("legacy-key", "credential"))
	r := &Reconciler{}
	if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
		t.Fatal(err)
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "full"}, &connection); err != nil {
		t.Fatal(err)
	}
	var stored aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Spec.LLM.Models) != 200 || stored.Spec.LLM.Models[0].ConnectionRef != "full" || stored.Spec.LLM.Models[0].Archived || len(stored.Status.LegacyModelMigrations) != 1 {
		t.Fatal("capacity-limited migration lost the source or grew revision history")
	}
}

func TestSharedLegacyKeyMigratesOnlyTheOriginallyObservedCohort(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-uid", "first", "shared-key", "https://api.openai.com/v1")
	second := studio.Spec.LLM.Models[0]
	second.ID, second.Name, second.RevisionID = "second", "Second", "second-r1"
	second.BaseURL = "https://gateway.example/v1"
	studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, second)
	c := newMigrationClient(t, studio, legacyKeySecret("shared-key", "credential"))
	r := &Reconciler{}
	if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
		t.Fatal(err)
	}
	var current aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.LegacyModelMigrations) != 2 {
		t.Fatal("shared-key cohort was not captured before the first migration")
	}
	forged := second
	forged.ID, forged.Name, forged.RevisionID, forged.BaseURL = "forged", "Forged", "forged-r1", "https://attacker.example/v1"
	current.Spec.LLM.Models = append(current.Spec.LLM.Models, forged)
	if err := c.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &current); err != nil {
		t.Fatal(err)
	}
	active := map[string]aiv1alpha1.StudioLLMModel{}
	for _, model := range current.Spec.LLM.Models {
		if !model.Archived {
			active[model.ID] = model
		}
	}
	if active["first"].ConnectionRef != "first" || active["second"].ConnectionRef != "second" || active["second"].ConnectionSnapshot.BaseURL != second.BaseURL {
		t.Fatal("original shared-key models did not retain their independent bindings and endpoints")
	}
	if active["forged"].ConnectionRef != "" {
		t.Fatal("a caller-added model joined the trusted migration cohort")
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "forged"}, &connection); !apierrors.IsNotFound(err) {
		t.Fatal("untrusted entry created a connection")
	}
}

func TestConnectionAdmissionPreservesOldRuntimeWhenConcurrentHistoryGrowthBlocksRollover(t *testing.T) {
	ctx := context.Background()
	oldID, newID := "0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789"
	connection := connectionForSnapshot("primary", "connection-uid", "https://new.example/v1", aiv1alpha1.ModelProviderConnectionSecretPrefix+newID, newID)
	oldSecret, newSecret := generatedSecret(oldID, "old", connection), generatedSecret(newID, "new", connection)
	oldSnapshot, err := api.ModelProviderConnectionSnapshot(connectionForSnapshot("primary", connection.UID, "https://old.example/v1", oldSecret.Name, oldID), secretObject(t, oldSecret))
	if err != nil {
		t.Fatal(err)
	}
	connection.Status.ActiveSnapshot = oldSnapshot
	studio := legacyStudio("studio-uid", "fast", "unused", "https://old.example/v1")
	studio.Spec.LLM.Models[0].ConnectionRef, studio.Spec.LLM.Models[0].ConnectionSnapshot, studio.Spec.LLM.Models[0].SecretRef = "primary", oldSnapshot, nil
	for i := 1; i < 200; i++ {
		archived := studio.Spec.LLM.Models[0]
		archived.RevisionID = fmt.Sprintf("history-%d", i)
		archived.Archived = true
		studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, archived)
	}
	c := newMigrationClient(t, studio, connection, oldSecret, newSecret)
	r := &Reconciler{}
	if admitted, err := r.admitConnectionSnapshot(ctx, c, c, connection, newSecret); !errors.Is(err, errRevisionHistoryFull) || admitted != nil {
		t.Fatalf("full registry admitted desired config: snapshot=%v err=%v", admitted, err)
	}
	if connection.Status.ActiveSnapshot.SecretRef.Name != oldSecret.Name {
		t.Fatal("failed rollover changed the admitted runtime key")
	}
	var current aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.LLM.Models = current.Spec.LLM.Models[:199]
	if err := c.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.admitConnectionSnapshot(ctx, c, c, connection, newSecret)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.SecretRef.Name != newSecret.Name {
		t.Fatal("successful rollover did not admit the replacement")
	}
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Spec.LLM.Models) != 200 || current.Spec.LLM.Models[199].ConnectionSnapshot.ConnectionFingerprint != admitted.ConnectionFingerprint {
		t.Fatal("admission did not first persist the new binding revision")
	}
}

func TestSharedLegacyMigrationAtHistoryBoundaryDoesNotStrandSibling(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-uid", "first", "shared-key", "https://api.openai.com/v1")
	second := studio.Spec.LLM.Models[0]
	second.ID, second.RevisionID = "second", "second-r1"
	studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, second)
	for i := 0; i < 197; i++ {
		prior := second
		prior.RevisionID = fmt.Sprintf("history-%d", i)
		prior.Archived = true
		studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, prior)
	}
	c := newMigrationClient(t, studio, legacyKeySecret("shared-key", "credential"))
	r := &Reconciler{}
	for i := 0; i < 3; i++ {
		if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
			t.Fatal(err)
		}
	}
	var current aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &current); err != nil {
		t.Fatal(err)
	}
	active := map[string]string{}
	for _, model := range current.Spec.LLM.Models {
		if !model.Archived {
			active[model.ID] = model.ConnectionRef
		}
	}
	if len(current.Spec.LLM.Models) != 200 || active["first"] != "first" || active["second"] != "second" {
		t.Fatal("last available archive slot stranded a shared-key model")
	}
}

func TestRolloverRepairsUnnormalizedSnapshotWithMatchingFingerprint(t *testing.T) {
	ctx := context.Background()
	connection := connectionForSnapshot("primary", "connection-uid", "https://api.openai.com/v1/", "external-key", "")
	secret := legacyKeySecret("external-key", "credential")
	admitted, err := api.ModelProviderConnectionSnapshot(connection, secretObject(t, secret))
	if err != nil {
		t.Fatal(err)
	}
	raw := admitted.DeepCopy()
	raw.BaseURL = connection.Spec.BaseURL
	studio := legacyStudio("studio-uid", "fast", secret.Name, connection.Spec.BaseURL)
	studio.Spec.LLM.Models[0].ConnectionRef = connection.Name
	studio.Spec.LLM.Models[0].ConnectionSnapshot = raw
	c := newMigrationClient(t, studio, connection, secret)
	r := &Reconciler{}
	if err := r.rolloverBindings(ctx, c, c, connection, secret); err != nil {
		t.Fatal(err)
	}
	var observed aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &observed); err != nil {
		t.Fatal(err)
	}
	if len(observed.Spec.LLM.Models) != 2 {
		t.Fatal("matching fingerprint prevented repair of the unnormalized snapshot")
	}
	for _, model := range observed.Spec.LLM.Models {
		if !model.Archived && model.ConnectionSnapshot.BaseURL != admitted.BaseURL {
			t.Fatal("runtime snapshot was not normalized")
		}
	}
}

func TestUnmarkedConnectionCannotResolveGeneratedKey(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	victim := connectionForSnapshot("restricted", "restricted-uid", "https://api.openai.com/v1", aiv1alpha1.ModelProviderConnectionSecretPrefix+id, id)
	key := generatedSecret(id, "synthetic-key", victim)
	attacker := connectionForSnapshot("new-account", "new-account-uid", "https://attacker.example/v1", key.Name, "")
	c := newMigrationClient(t, victim, key, attacker)
	_, _, why, _, err := resolveSecret(context.Background(), c, attacker)
	if err != nil {
		t.Fatal(err)
	}
	if why != "SecretOwnershipInvalid" {
		t.Fatal("unmarked connection accepted another connection's generated key")
	}
}

func TestExternalCredentialProtectionSurvivesRotationAndConnectionDeletion(t *testing.T) {
	ctx := context.Background()
	studio := legacyStudio("studio-uid", "forged", "retained-external-key", "https://attacker.example/v1")
	oldKey := legacyKeySecret("retained-external-key", "synthetic-key")
	newKey := legacyKeySecret("replacement-key", "replacement")
	connection := connectionForSnapshot("restricted", "restricted-uid", "https://api.openai.com/v1", newKey.Name, "")
	connection.Status.ActiveSnapshot = &aiv1alpha1.StudioLLMConnectionSnapshot{SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: oldKey.Name}, SecretUID: string(oldKey.UID)}
	c := newMigrationClient(t, studio, oldKey, newKey, connection)
	if err := protectExternalCredentials(ctx, c, c, connection, newKey); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, connection); err != nil {
		t.Fatal(err)
	}
	var observed aiv1alpha1.Studio
	if err := c.Get(ctx, types.NamespacedName{Name: studio.Name}, &observed); err != nil {
		t.Fatal(err)
	}
	if len(observed.Status.ProtectedModelCredentials) != 2 {
		t.Fatal("previous and desired external keys were not retained")
	}
	r := &Reconciler{}
	if err := r.migrateLegacyBindings(ctx, c, c); err != nil {
		t.Fatal(err)
	}
	var forged aiv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, types.NamespacedName{Name: "forged"}, &forged); !apierrors.IsNotFound(err) {
		t.Fatal("retained external key was migrated into a forged account")
	}
	var remaining corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: oldKey.Name}, &remaining); err != nil {
		t.Fatal(err)
	}
	if len(remaining.OwnerReferences) != 0 {
		t.Fatal("external key was adopted")
	}
}
