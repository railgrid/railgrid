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
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// AuthorizeModelProviderConnectionUse separates visibility from use of a paid
// provider account. Attended verbs check the stamped caller; caller-scoped MCP
// uses SelfSubjectAccessReview. Unattended controllers run configured Agents as
// the provider and have no caller to impersonate.
func AuthorizeModelProviderConnectionUse(ctx context.Context, d dynamic.Interface, name string, callerScoped bool) error {
	var allowed bool
	var err error
	if identity, ok := dataplane.ProxiedIdentityFrom(ctx); ok {
		allowed, err = dataplane.Authorize(ctx, d, identity, dataplane.ResourceAttributes{Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version, Resource: "modelproviderconnections", Name: name, Verb: "use"})
	} else if callerScoped {
		review := &authorizationv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}, Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version, Resource: "modelproviderconnections", Name: name, Verb: "use"}}}
		var object map[string]interface{}
		object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(review)
		if err == nil {
			var result *unstructured.Unstructured
			result, err = d.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctx, &unstructured.Unstructured{Object: object}, metav1.CreateOptions{})
			if err == nil {
				allowed, _, err = unstructured.NestedBool(result.Object, "status", "allowed")
			}
		}
	} else {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking provider connection use: %w", err)
	}
	if !allowed {
		return apierrors.NewForbidden(ModelProviderConnectionGVR.GroupResource(), name, fmt.Errorf("use permission is required"))
	}
	return nil
}
