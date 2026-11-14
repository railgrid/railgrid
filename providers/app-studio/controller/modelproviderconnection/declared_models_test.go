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
	"fmt"
	"reflect"
	"testing"

	api "github.com/railgrid/provider-app-studio/api"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// seededConnection is a connection the way a WorkspaceSeed writes one: an
// external Secret somebody else owns, and the models declared on the spec.
func seededConnection(t *testing.T, models ...aiv1alpha1.ModelProviderConnectionModel) (*aiv1alpha1.ModelProviderConnection, *corev1.Secret, *aiv1alpha1.StudioLLMConnectionSnapshot) {
	t.Helper()
	connection := &aiv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "railgrid", UID: types.UID("connection-uid"), Generation: 1},
		Spec: aiv1alpha1.ModelProviderConnectionSpec{
			Provider: "openai-compatible", BaseURL: "http://switchboard.example/v1",
			SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: "switchboard-app-studio"},
			Models:    models,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "switchboard-app-studio", Namespace: "default", UID: types.UID("secret-uid"), ResourceVersion: "7",
			Labels: map[string]string{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
		},
		Data: map[string][]byte{aiv1alpha1.ModelProviderConnectionSecretKey: []byte("key")},
	}
	snapshot, err := api.ModelProviderConnectionSnapshot(connection, secretObject(t, secret))
	if err != nil {
		t.Fatal(err)
	}
	return connection, secret, snapshot
}

func emptyStudio() *aiv1alpha1.Studio {
	return &aiv1alpha1.Studio{ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName, UID: types.UID("studio-uid")}}
}

func readStudio(t *testing.T, c client.Client) *aiv1alpha1.Studio {
	t.Helper()
	var studio aiv1alpha1.Studio
	if err := c.Get(context.Background(), types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		t.Fatal(err)
	}
	return &studio
}

func activeModels(studio *aiv1alpha1.Studio) []aiv1alpha1.StudioLLMModel {
	var out []aiv1alpha1.StudioLLMModel
	if studio.Spec.LLM == nil {
		return nil
	}
	for _, model := range studio.Spec.LLM.Models {
		if !model.Archived {
			out = append(out, model)
		}
	}
	return out
}

func TestDeclaredModelIsBoundOnceAndBecomesTheDefault(t *testing.T) {
	ctx := context.Background()
	connection, secret, snapshot := seededConnection(t, aiv1alpha1.ModelProviderConnectionModel{ID: "default", Model: "default", Name: "Railgrid", Default: true})
	c := newMigrationClient(t, emptyStudio(), connection, secret)
	r := &Reconciler{}

	offered, err := r.bindDeclaredModels(ctx, c, c, connection, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offered, []string{"default"}) {
		t.Fatalf("offered = %v, want [default]", offered)
	}
	studio := readStudio(t, c)
	active := activeModels(studio)
	if len(active) != 1 {
		t.Fatalf("active bindings = %d, want 1", len(active))
	}
	got := active[0]
	if got.ID != "default" || got.Model != "default" || got.Name != "Railgrid" || got.ConnectionRef != "railgrid" || got.RevisionID == "" {
		t.Fatalf("binding = %+v", got)
	}
	if !completeSnapshot(got.ConnectionSnapshot) || !reflect.DeepEqual(got.ConnectionSnapshot, snapshot) {
		t.Fatalf("binding snapshot = %+v, want the admitted snapshot %+v", got.ConnectionSnapshot, snapshot)
	}
	if studio.Spec.LLM.DefaultModel != "default" {
		t.Fatalf("defaultModel = %q, want default", studio.Spec.LLM.DefaultModel)
	}

	// A binding carrying the admitted snapshot is exactly what rollover leaves
	// alone, so the two do not take turns archiving each other's work.
	if err := r.rolloverBindings(ctx, c, c, connection, secret); err != nil {
		t.Fatal(err)
	}
	if again := readStudio(t, c); len(again.Spec.LLM.Models) != 1 {
		t.Fatalf("rollover rewrote a freshly declared binding: %d revisions", len(again.Spec.LLM.Models))
	}

	// Offered once: a second pass writes nothing.
	offered, err = r.bindDeclaredModels(ctx, c, c, connection, snapshot, offered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offered, []string{"default"}) || len(readStudio(t, c).Spec.LLM.Models) != 1 {
		t.Fatalf("second pass changed the registry (offered %v)", offered)
	}
}

func TestDeclaredModelIsNotPutBackAfterTheWorkspaceRemovesIt(t *testing.T) {
	ctx := context.Background()
	connection, secret, snapshot := seededConnection(t, aiv1alpha1.ModelProviderConnectionModel{ID: "default", Model: "default", Default: true})
	c := newMigrationClient(t, emptyStudio(), connection, secret)
	r := &Reconciler{}
	offered, err := r.bindDeclaredModels(ctx, c, c, connection, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}

	studio := readStudio(t, c)
	studio.Spec.LLM.Models = nil
	studio.Spec.LLM.DefaultModel = ""
	if err := c.Update(ctx, studio); err != nil {
		t.Fatal(err)
	}

	if _, err := r.bindDeclaredModels(ctx, c, c, connection, snapshot, offered); err != nil {
		t.Fatal(err)
	}
	if after := readStudio(t, c); len(after.Spec.LLM.Models) != 0 || after.Spec.LLM.DefaultModel != "" {
		t.Fatalf("a removed declared binding came back: %+v", after.Spec.LLM)
	}
}

func TestDeclaredModelLeavesAnExistingBindingAndDefaultAlone(t *testing.T) {
	ctx := context.Background()
	connection, secret, snapshot := seededConnection(t,
		aiv1alpha1.ModelProviderConnectionModel{ID: "default", Model: "default", Default: true},
		aiv1alpha1.ModelProviderConnectionModel{ID: "extra", Model: "upstream-extra", Default: true},
	)
	studio := emptyStudio()
	studio.Spec.LLM = &aiv1alpha1.StudioLLM{
		DefaultModel: "mine",
		Models: []aiv1alpha1.StudioLLMModel{
			{ID: "mine", RevisionID: "rev-mine", Name: "Mine", Model: "gpt-x", ConnectionRef: "other"},
			{ID: "default", RevisionID: "rev-default", Name: "My default", Model: "gpt-y", ConnectionRef: "other"},
		},
	}
	c := newMigrationClient(t, studio, connection, secret)

	offered, err := (&Reconciler{}).bindDeclaredModels(ctx, c, c, connection, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offered, []string{"default", "extra"}) {
		t.Fatalf("offered = %v, want both ids recorded", offered)
	}
	after := readStudio(t, c)
	if after.Spec.LLM.DefaultModel != "mine" {
		t.Fatalf("defaultModel = %q; the workspace's own default was replaced", after.Spec.LLM.DefaultModel)
	}
	byID := map[string]aiv1alpha1.StudioLLMModel{}
	for _, model := range activeModels(after) {
		byID[model.ID] = model
	}
	if len(byID) != 3 {
		t.Fatalf("active bindings = %d, want 3", len(byID))
	}
	if byID["default"].RevisionID != "rev-default" || byID["default"].ConnectionRef != "other" {
		t.Fatalf("the workspace's own binding of id default was touched: %+v", byID["default"])
	}
	if byID["extra"].Model != "upstream-extra" || byID["extra"].Name != "extra" || byID["extra"].ConnectionRef != "railgrid" {
		t.Fatalf("declared binding extra = %+v", byID["extra"])
	}
}

func TestDeclaredModelWaitsForTheStudioAndForRoom(t *testing.T) {
	ctx := context.Background()
	connection, secret, snapshot := seededConnection(t, aiv1alpha1.ModelProviderConnectionModel{ID: "default", Model: "default"})

	// No Studio yet: nothing is recorded, so the Studio's creation retries it.
	c := newMigrationClient(t, connection, secret)
	offered, err := (&Reconciler{}).bindDeclaredModels(ctx, c, c, connection, snapshot, nil)
	if err != nil || len(offered) != 0 {
		t.Fatalf("without a Studio: offered %v, err %v; want nothing recorded", offered, err)
	}

	// A full registry: likewise not recorded, and not written.
	studio := emptyStudio()
	studio.Spec.LLM = &aiv1alpha1.StudioLLM{}
	for i := range maxActiveStudioModels {
		id := fmt.Sprintf("m%d", i)
		studio.Spec.LLM.Models = append(studio.Spec.LLM.Models, aiv1alpha1.StudioLLMModel{ID: id, RevisionID: "rev-" + id, Name: id, Model: id})
	}
	c = newMigrationClient(t, studio, connection, secret)
	offered, err = (&Reconciler{}).bindDeclaredModels(ctx, c, c, connection, snapshot, nil)
	if err != nil || len(offered) != 0 {
		t.Fatalf("with a full registry: offered %v, err %v; want nothing recorded", offered, err)
	}
	if got := len(readStudio(t, c).Spec.LLM.Models); got != maxActiveStudioModels {
		t.Fatalf("registry grew past its bound: %d", got)
	}
}

func TestUndeclaredIDIsForgottenSoDeclaringItAgainOffersItAgain(t *testing.T) {
	ctx := context.Background()
	connection, secret, snapshot := seededConnection(t, aiv1alpha1.ModelProviderConnectionModel{ID: "default", Model: "default"})
	c := newMigrationClient(t, emptyStudio(), connection, secret)

	offered, err := (&Reconciler{}).bindDeclaredModels(ctx, c, c, connection, snapshot, []string{"gone", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offered, []string{"default"}) {
		t.Fatalf("offered = %v, want the undeclared id dropped", offered)
	}
}
