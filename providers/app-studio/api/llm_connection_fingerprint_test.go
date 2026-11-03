// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

func fingerprintFixtures() (*aiv1alpha1.ModelProviderConnection, *unstructured.Unstructured) {
	return &aiv1alpha1.ModelProviderConnection{
			ObjectMeta: metav1.ObjectMeta{Name: "account", UID: types.UID("account-uid"), Generation: 1},
			Spec: aiv1alpha1.ModelProviderConnectionSpec{
				Provider: "openai-compatible", BaseURL: "https://example.test/v1",
				SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: "account-key"},
			},
		}, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
			"metadata":   map[string]interface{}{"name": "account-key", "uid": "key-uid", "resourceVersion": "42", "labels": map[string]interface{}{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner}},
			"stringData": map[string]interface{}{"apiKey": "fixture-key-never-in-public-metadata"},
		}}
}

func TestConnectionFingerprintInvalidatesChangedDestinationAndKeyIdentity(t *testing.T) {
	connection, secret := fingerprintFixtures()
	original, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*aiv1alpha1.ModelProviderConnection, *unstructured.Unstructured)
	}{
		{"connection replacement", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) { c.UID = "new-account-uid" }},
		{"generation", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) { c.Generation++ }},
		{"provider", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) {
			c.Spec.Provider = "google-ai-studio"
		}},
		{"endpoint", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) {
			c.Spec.BaseURL = "https://other.test/v1"
		}},
		{"workspace", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) {
			c.Spec.AnthropicWorkspaceID = "wrkspc_other"
		}},
		{"secret reference", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) {
			c.Spec.SecretRef.Name = "other-key"
		}},
		{"secret entry", func(c *aiv1alpha1.ModelProviderConnection, _ *unstructured.Unstructured) { c.Spec.SecretKey = "other" }},
		{"secret replacement", func(_ *aiv1alpha1.ModelProviderConnection, s *unstructured.Unstructured) { s.SetUID("new-key-uid") }},
		{"secret version", func(_ *aiv1alpha1.ModelProviderConnection, s *unstructured.Unstructured) { s.SetResourceVersion("43") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, key := connection.DeepCopy(), secret.DeepCopy()
			test.change(candidate, key)
			got, err := ModelProviderConnectionFingerprint(candidate, key)
			if err != nil {
				t.Fatal(err)
			}
			if got == original {
				t.Fatal("a changed destination or credential identity retained the verified fingerprint")
			}
		})
	}
}

func TestConnectionFingerprintAndSnapshotContainNoCredentialMaterial(t *testing.T) {
	connection, secret := fingerprintFixtures()
	original, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	otherKey := secret.DeepCopy()
	otherKey.Object["stringData"] = map[string]interface{}{"apiKey": "different-fixture-key"}
	got, err := ModelProviderConnectionFingerprint(connection, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if got != original {
		t.Fatal("public fingerprint derives from key bytes rather than Kubernetes identity")
	}
	snapshot, err := ModelProviderConnectionSnapshot(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-key-never-in-public-metadata") || strings.Contains(string(encoded), "stringData") {
		t.Fatal("the persisted model revision snapshot contains credential material")
	}
	if snapshot.ConnectionUID != "account-uid" || snapshot.SecretUID != "key-uid" || snapshot.SecretResourceVersion != "42" || snapshot.ConnectionFingerprint != original {
		t.Fatalf("snapshot did not pin both resource identities: %+v", snapshot)
	}
	connection.Spec.BaseURL = "https://changed.test/v1"
	secret.SetUID("replacement-key")
	if snapshot.BaseURL != "https://example.test/v1" || snapshot.SecretUID != "key-uid" {
		t.Fatal("changing a connection mutated an existing revision snapshot")
	}
}
