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
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	clienttesting "k8s.io/client-go/testing"
)

func TestCallerExecAuthorizerUsesCallerScopedSelfSubjectAccessReview(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateAction)
		review := create.GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		if attrs == nil || attrs.Group != execInstanceGroupVersion.Group || attrs.Version != execInstanceGroupVersion.Version ||
			attrs.Resource != "applications" || attrs.Subresource != "exec" || attrs.Name != "demo" || attrs.Verb != "create" {
			t.Fatalf("resource attributes = %#v", attrs)
		}
		review.Status.Allowed = true
		return true, review, nil
	})
	factory := &fakeCallerAuthorizationFactory{client: client.AuthorizationV1()}
	authorizer := NewCallerExecAuthorizer(factory)
	err := authorizer.AuthorizeExec(context.Background(), ExecAuthorization{
		Workspace: "cluster-id", CallerToken: "caller-token", Resource: "applications", Name: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.cluster != "cluster-id" || factory.token != "caller-token" {
		t.Fatalf("factory called with cluster=%q token=%q", factory.cluster, factory.token)
	}
}

func TestCallerExecAuthorizerFailsClosedOnDeniedReview(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		review.Status.Allowed = false
		review.Status.Reason = "policy denied"
		return true, review, nil
	})
	authorizer := NewCallerExecAuthorizer(&fakeCallerAuthorizationFactory{client: client.AuthorizationV1()})
	err := authorizer.AuthorizeExec(context.Background(), ExecAuthorization{Workspace: "cluster", CallerToken: "token", Resource: "applications", Name: "demo"})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("AuthorizeExec error = %v, want forbidden", err)
	}
}

type fakeCallerAuthorizationFactory struct {
	client  authorizationv1client.AuthorizationV1Interface
	cluster string
	token   string
}

func (f *fakeCallerAuthorizationFactory) AuthorizationFor(cluster, token string) (authorizationv1client.AuthorizationV1Interface, error) {
	f.cluster = cluster
	f.token = token
	return f.client, nil
}
