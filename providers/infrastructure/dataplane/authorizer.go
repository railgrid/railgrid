/*
Copyright 2026 The Faros Authors.

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

package dataplane

import (
	"context"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

var execInstanceGroupVersion = schema.GroupVersion{Group: "infrastructure.faros.sh", Version: "v1alpha1"}

// CallerAuthorizationClientFactory builds an authorization client scoped to
// the tenant logical cluster and authenticated solely with the forwarded
// caller token.
type CallerAuthorizationClientFactory interface {
	AuthorizationFor(clusterID, token string) (authorizationv1client.AuthorizationV1Interface, error)
}

type callerExecAuthorizer struct {
	factory CallerAuthorizationClientFactory
}

// NewCallerExecAuthorizer requires explicit create permission on the instance
// kind's exec subresource, in addition to the handler's caller-scoped GET.
func NewCallerExecAuthorizer(factory CallerAuthorizationClientFactory) ExecAuthorizer {
	return &callerExecAuthorizer{factory: factory}
}

func (a *callerExecAuthorizer) AuthorizeExec(ctx context.Context, request ExecAuthorization) error {
	if a == nil || a.factory == nil {
		return fmt.Errorf("exec authorizer is unavailable")
	}
	client, err := a.factory.AuthorizationFor(request.Workspace, request.CallerToken)
	if err != nil {
		return err
	}
	review, err := client.SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Group: execInstanceGroupVersion.Group, Version: execInstanceGroupVersion.Version,
			Resource: request.Resource, Subresource: "exec", Name: request.Name, Verb: "create",
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("authorize instance exec as caller: %w", err)
	}
	if !review.Status.Allowed {
		reason := strings.TrimSpace(review.Status.Reason)
		if reason == "" {
			reason = "caller is not allowed to execute commands for this instance"
		}
		return apierrors.NewForbidden(execInstanceGroupVersion.WithResource(request.Resource).GroupResource(), request.Name, fmt.Errorf("%s", reason))
	}
	return nil
}
