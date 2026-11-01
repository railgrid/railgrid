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

package api

import (
	"testing"

	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestRepeatedModelDeletePreservesConnectionKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = agentsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	model := &agentsv1alpha1.ModelCredential{TypeMeta: metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"}, ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "model"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account", Model: "one"}}
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "connection", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{BaseURL: "https://provider.example/v1", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: CredentialSecretName("account")}}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialSecretName("account"), Namespace: "default", OwnerReferences: []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: "account", UID: connection.UID}}}, Data: map[string][]byte{"apiKey": []byte("test-only")}}
	d := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{agentsclient.ModelCredentialGVR: "ModelCredentialList", agentsclient.ModelProviderConnectionGVR: "ModelProviderConnectionList"}, model, connection, key)
	c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d))
	if err := deleteCredential(t.Context(), c, "account"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSecret(t.Context(), "default", key.Name); err != nil {
		t.Fatal("first deletion removed key", err)
	}
	if err := deleteCredential(t.Context(), c, "account"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSecret(t.Context(), "default", key.Name); err != nil {
		t.Fatalf("retry of already-deleted model removed connection-owned key: %v", err)
	}
}

func TestLegacyModelDeletionOnlyCleansItsExclusivelyOwnedKey(t *testing.T) {
	for _, ownership := range []string{"legacy", "connection", "unowned", "shared"} {
		t.Run(ownership, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = agentsv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			model := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "legacy", UID: "legacy-uid", ResourceVersion: "model-version"}, Spec: agentsv1alpha1.ModelCredentialSpec{BaseURL: "https://provider.example/v1", Model: "one", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "key"}}}
			owner := metav1.OwnerReference{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: model.Name, UID: model.UID}
			key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "default", UID: "key-uid", ResourceVersion: "key-version"}, Data: map[string][]byte{"apiKey": []byte("test-only")}}
			switch ownership {
			case "legacy":
				key.OwnerReferences = []metav1.OwnerReference{owner}
			case "connection":
				owner.Kind = "ModelProviderConnection"
				owner.UID = "connection-uid"
				key.OwnerReferences = []metav1.OwnerReference{owner}
			case "shared":
				key.OwnerReferences = []metav1.OwnerReference{owner, {APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: "account", UID: "connection-uid"}}
			}
			d := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{agentsclient.ModelCredentialGVR: "ModelCredentialList"}, model, key)
			c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d))
			if err := deleteCredential(t.Context(), c, model.Name); err != nil {
				t.Fatal(err)
			}
			_, err := c.GetSecret(t.Context(), "default", key.Name)
			if ownership == "legacy" {
				if err == nil {
					t.Fatal("owned legacy key was not cleaned up")
				}
			} else if err != nil {
				t.Fatal("unowned/shared key was deleted", err)
			}
			for _, action := range d.Actions() {
				if action.GetVerb() != "delete" {
					continue
				}
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil {
					t.Fatal("delete lost object preconditions")
				}
			}
		})
	}
}
