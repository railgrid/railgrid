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
	"fmt"
	"testing"

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestMCPUpdatesBindingWithoutTouchingConnectionOrKey(t *testing.T) {
	for _, variant := range []string{"model-only", "provider", "endpoint", "key", "workspace", "conflict"} {
		t.Run(variant, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = agents.AddToScheme(scheme)
			original := &agents.ModelCredential{TypeMeta: metav1.TypeMeta{APIVersion: agents.SchemeGroupVersion.String(), Kind: "ModelCredential"}, ObjectMeta: metav1.ObjectMeta{Name: "existing", UID: "model-uid", ResourceVersion: "7", Annotations: map[string]string{"keep": "annotation"}}, Spec: agents.ModelCredentialSpec{ConnectionRef: "account", Model: "old-model"}}
			d := dynamicfake.NewSimpleDynamicClient(scheme, original)
			d.PrependReactor("*", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetResource().Resource != "modelcredentials" {
					t.Fatalf("model-only update touched %s", a.GetResource().Resource)
				}
				if a.GetVerb() == "update" {
					obj := a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
					if obj.GetResourceVersion() != "7" || obj.GetUID() != "model-uid" {
						t.Fatal("update discarded concurrency preconditions")
					}
					if variant == "conflict" {
						return true, nil, apierrors.NewConflict(agentsclient.ModelCredentialGVR.GroupResource(), original.Name, fmt.Errorf("concurrent rebind"))
					}
				}
				return false, nil, nil
			})
			req := &modelCredential{Name: original.Name, Model: "new-model"}
			switch variant {
			case "provider":
				req.Provider = "openai"
			case "endpoint":
				req.BaseURL = "https://other/v1"
			case "key":
				req.APIKey = "replacement"
			case "workspace":
				req.AnthropicWorkspaceID = "wrkspc_other"
			}
			c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d))
			result, err := applyCredentialUpsert(t.Context(), c, req)
			if variant == "conflict" && !apierrors.IsConflict(err) {
				t.Fatalf("conflict not propagated: %v", err)
			}
			if variant == "model-only" && (err != nil || result.Model != "new-model") {
				t.Fatalf("model update failed: %+v %v", result, err)
			}
			if variant != "model-only" && err == nil {
				t.Fatal("unsafe update accepted")
			}
			stored, readErr := c.ModelCredentials().Get(t.Context(), original.Name, metav1.GetOptions{})
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "old-model"
			if variant == "model-only" {
				want = "new-model"
			}
			if stored.Spec.Model != want || stored.Spec.ConnectionRef != "account" || stored.Annotations["keep"] != "annotation" {
				t.Fatalf("binding changed unexpectedly: %+v", stored)
			}
		})
	}
}
