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

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestModelSummaryDoesNotRequireUseOrSecrets(t *testing.T) {
	for _, readable := range []bool{true, false} {
		t.Run(fmt.Sprint(readable), func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = agentsv1alpha1.AddToScheme(scheme)
			model := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "model"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account", Model: "one"}, Status: agentsv1alpha1.ModelCredentialStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}}
			connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account"}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{Provider: "openai", BaseURL: "https://provider.example/v1"}, Status: agentsv1alpha1.ModelCredentialStatus{Models: []string{"one", "two"}}}
			d := dynamicfake.NewSimpleDynamicClient(scheme, model, connection)
			d.PrependReactor("*", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
				resource := a.GetResource().Resource
				if resource == "secrets" || resource == "selfsubjectaccessreviews" {
					t.Fatalf("metadata listing requested %s", resource)
				}
				if resource == "modelproviderconnections" && !readable {
					return true, nil, apierrors.NewForbidden(agentsclient.ModelProviderConnectionGVR.GroupResource(), "account", fmt.Errorf("private"))
				}
				return false, nil, nil
			})
			out, err := listModelCredentialSummaries(t.Context(), agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d)))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Credentials) != 1 || out.Credentials[0].Name != "model" || !out.Credentials[0].Ready {
				t.Fatalf("visible model missing: %+v", out)
			}
			if readable && (out.Credentials[0].Provider != "openai" || len(out.Credentials[0].Models) != 2) {
				t.Fatalf("parent metadata/catalog missing: %+v", out)
			}
		})
	}
}
