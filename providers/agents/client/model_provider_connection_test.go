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

package client

import (
	"context"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-sdk/dataplane"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestConnectionUseIsIndependentOfGetPermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{true: "allowed", false: "get-only"}[allowed], func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			connection := &agentsv1alpha1.ModelProviderConnection{TypeMeta: metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection"}, ObjectMeta: metav1.ObjectMeta{Name: "account"}}
			d := dynamicfake.NewSimpleDynamicClient(scheme, connection)
			checked := false
			d.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
				object := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				var review authorizationv1.SelfSubjectAccessReview
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &review); err != nil {
					t.Fatal(err)
				}
				attrs := review.Spec.ResourceAttributes
				if attrs.Verb != "use" || attrs.Resource != "modelproviderconnections" || attrs.Name != "account" {
					t.Fatalf("wrong permission check: %+v", attrs)
				}
				checked = true
				return true, &unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{"allowed": allowed}}}, nil
			})
			c := NewFromScope(tenant.NewScopeFromDynamic("tenant", d))
			if _, err := c.ModelProviderConnections().Get(context.Background(), "account", metav1.GetOptions{}); err != nil {
				t.Fatal("configuration read must be independent", err)
			}
			_, err := c.GetModelProviderConnection(context.Background(), "account")
			if !checked {
				t.Fatal("missing use check")
			}
			if allowed && err != nil {
				t.Fatal(err)
			}
			if !allowed && !apierrors.IsForbidden(err) {
				t.Fatal("get-only access allowed use", err)
			}
		})
	}
}

// A run admitted through an automation grant (start-run/v1) spends the agent's
// configured models under delegation, like a Schedule or Trigger run. The
// stamped caller, an App Studio Project's workload identity, can never hold
// `use`, so the check must not ask about it. Without the marker the same
// caller is still refused.
func TestDelegatedModelUseSkipsTheAttendedCallerCheck(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := dynamicfake.NewSimpleDynamicClient(scheme)
	reviews := 0
	d.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		reviews++
		return true, &unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{"allowed": false}}}, nil
	})
	attended := dataplane.WithProxiedIdentity(context.Background(), dataplane.ProxiedIdentity{User: "system:serviceaccount:default:railgrid-wi-project"})

	if err := AuthorizeModelProviderConnectionUse(attended, d, "account", false); !apierrors.IsForbidden(err) {
		t.Fatalf("attended caller without use: err = %v, want forbidden", err)
	}
	if reviews != 1 {
		t.Fatalf("attended check made %d reviews, want 1", reviews)
	}

	if err := AuthorizeModelProviderConnectionUse(WithDelegatedModelUse(attended), d, "account", false); err != nil {
		t.Fatalf("delegated run: err = %v, want nil", err)
	}
	if reviews != 1 {
		t.Fatal("a delegated run still reviewed the caller")
	}
}
