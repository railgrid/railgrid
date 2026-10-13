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

package appauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	k8stesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	brokerTestProjectUID  = "project-uid-123"
	brokerTestInstanceUID = "instance-uid-123"
)

func TestAppAccessGrantBrokerRejectsCrossScopeAndUnboundRequests(t *testing.T) {
	tests := []struct {
		name   string
		call   func() tenant.DelegatedCall
		org    string
		ws     string
		body   string
		status int
	}{
		{name: "cross workspace", call: brokerListCall, org: "org", ws: "other-workspace", body: brokerListBody(), status: http.StatusForbidden},
		{name: "wrong project UID", call: brokerListCall, org: "org", ws: "workspace", body: strings.Replace(brokerListBody(), brokerTestProjectUID, "replacement-project", 1), status: http.StatusForbidden},
		{name: "wrong target UID", call: brokerListCall, org: "org", ws: "workspace", body: strings.Replace(brokerListBody(), brokerTestInstanceUID, "replacement-instance", 1), status: http.StatusForbidden},
		{name: "wrong proof operation", call: func() tenant.DelegatedCall {
			call := brokerListCall()
			call.AppAccessOperation = "create"
			call.AppAccessUser = "member"
			return call
		}, org: "org", ws: "workspace", body: brokerListBody(), status: http.StatusForbidden},
		{name: "missing capability", call: func() tenant.DelegatedCall { call := brokerListCall(); call.Capability = ""; return call }, org: "org", ws: "workspace", body: brokerListBody(), status: http.StatusForbidden},
		{name: "arbitrary RBAC input", call: brokerListCall, org: "org", ws: "workspace", body: strings.TrimSuffix(brokerListBody(), "}") + `,"rules":[{"verbs":["*"]}]}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := brokerTestClient(brokerTestInstanceUID)
			handler := brokerTestHandler(client, true)
			req := brokerTestRequest(test.body, test.org, test.ws, test.call())
			response := httptest.NewRecorder()
			handler.Handle(response, req)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestAppAccessGrantBrokerRejectsReplacedInstanceUID(t *testing.T) {
	client := brokerTestClient("replacement-instance-uid")
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	handler := brokerTestHandler(client, true)
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", brokerListCall()))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role.GetLabels()[appAccessInstanceUID] != "" || len(role.GetOwnerReferences()) != 0 {
		t.Fatalf("stale target caused a grant migration: labels=%#v owners=%#v", role.GetLabels(), role.GetOwnerReferences())
	}
}

func TestAppAccessGrantBrokerRejectsUnownedTargetBeforeMigration(t *testing.T) {
	const replacementProjectUID = "replacement-project-uid"
	client := brokerTestClientFor(replacementProjectUID, "replacement-instance-uid")
	instance, err := client.Resource(schema.GroupVersionResource{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}).Get(context.Background(), "app-demo-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	instance.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: "demo", UID: brokerTestProjectUID}})
	if _, err := client.Resource(schema.GroupVersionResource{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}).Update(context.Background(), instance, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	brokerSeedLegacyGrant(t, client, replacementProjectUID, "member", "railgrid:member@example.test")
	call := brokerListCall()
	call.AppAccessProjectUID = replacementProjectUID
	call.AppAccessUID = "replacement-instance-uid"
	body := strings.Replace(brokerListBody(), brokerTestProjectUID, replacementProjectUID, 1)
	body = strings.Replace(body, brokerTestInstanceUID, "replacement-instance-uid", 1)
	handler := brokerTestHandler(client, true)
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", call))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role.GetLabels()[appAccessInstanceUID] != "" || len(role.GetOwnerReferences()) != 0 {
		t.Fatalf("unowned target caused a grant migration: labels=%#v owners=%#v", role.GetLabels(), role.GetOwnerReferences())
	}
}

func TestAppAccessGrantBrokerRequiresHumanMutationAuthorization(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	handler := brokerTestHandler(client, false)
	call := brokerListCall()
	call.AppAccessOperation = "create"
	call.AppAccessUser = "member"
	body := `{"operation":"create","group":"infrastructure.railgrid.ai","resource":"instances","name":"app-demo-prod","uid":"` + brokerTestInstanceUID + `","projectName":"demo","projectUID":"` + brokerTestProjectUID + `","user":"member"}`
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", call))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	roles, err := client.Resource(appAccessClusterRoleGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := client.Resource(appAccessBindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(roles.Items) != 0 || len(bindings.Items) != 0 {
		t.Fatalf("unauthorized mutation created RBAC objects: roles=%d bindings=%d", len(roles.Items), len(bindings.Items))
	}
}

func TestAppAccessGrantBrokerCreatesOnlyFixedNamedGrant(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	handler := brokerTestHandler(client, true)
	call := brokerListCall()
	call.AppAccessOperation = "create"
	call.AppAccessUser = "member"
	body := `{"operation":"create","group":"infrastructure.railgrid.ai","resource":"instances","name":"app-demo-prod","uid":"` + brokerTestInstanceUID + `","projectName":"demo","projectUID":"` + brokerTestProjectUID + `","user":"member"}`
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", call))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	roles, err := client.Resource(appAccessClusterRoleGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := client.Resource(appAccessBindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(roles.Items) != 1 || len(bindings.Items) != 1 {
		t.Fatalf("RBAC objects = roles:%d bindings:%d, want one publishing role and primary binding", len(roles.Items), len(bindings.Items))
	}
	createdBindingName := appAccessUIDBindingName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID}, "member", appAccessGrantSourcePublishing)
	legacyBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), createdBindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := bindingSubjectUser(legacyBinding); got != "railgrid:member@example.test" {
		t.Fatalf("created grant subject = %q, want current account RBAC identity", got)
	}
	roleName := appAccessUIDRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID})
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	if len(rules) != 2 {
		t.Fatalf("legacy role rules = %#v, want only the existing reachability policy", rules)
	}
	resourceNames, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "resourceNames")
	verbs, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "verbs")
	if len(resourceNames) != 1 || resourceNames[0] != "app-demo-prod" || len(verbs) != 1 || verbs[0] != "get" {
		t.Fatalf("instance rule is not exact: %#v", rules[0])
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("publishing grant created a separate identity role: %v", err)
	}
	if role.GetLabels()[appAccessProjectUID] != brokerTestProjectUID || role.GetLabels()[appAccessInstanceUID] != brokerTestInstanceUID {
		t.Fatalf("role UID labels = %#v", role.GetLabels())
	}
	if owners := role.GetOwnerReferences(); len(owners) != 2 || owners[0].Kind != "Project" || owners[0].UID != brokerTestProjectUID || owners[1].Kind != "Instance" || owners[1].UID != brokerTestInstanceUID {
		t.Fatalf("role owner references = %#v", owners)
	}
	if owners := legacyBinding.GetOwnerReferences(); len(owners) != 2 || owners[0].Kind != "Project" || owners[0].UID != brokerTestProjectUID || owners[1].Kind != "Instance" || owners[1].UID != brokerTestInstanceUID {
		t.Fatalf("binding owner references = %#v", owners)
	}
	var result appAccessGrantResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].User != "member" || result.Items[0].Publication != "app-demo-prod" {
		t.Fatalf("response items = %#v", result.Items)
	}
}

func TestAppAccessGrantBrokerListsLegacyApprovalsWithoutRBACMigration(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	handler := brokerTestHandler(client, true)
	before := len(client.Actions())
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", brokerListCall()))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var result appAccessGrantResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].User != "member" || !result.Items[0].MigrationPending {
		t.Fatalf("legacy approvals = %#v", result.Items)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role.GetLabels()[appAccessProjectUID] != brokerTestProjectUID || role.GetLabels()[appAccessInstanceUID] != "" ||
		len(role.GetOwnerReferences()) != 0 {
		t.Fatalf("list migrated legacy role: labels=%#v owners=%#v", role.GetLabels(), role.GetOwnerReferences())
	}
	binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.GetLabels()[appAccessProjectUID] != brokerTestProjectUID || binding.GetLabels()[appAccessInstanceUID] != "" ||
		len(binding.GetOwnerReferences()) != 0 {
		t.Fatalf("list migrated legacy binding: labels=%#v owners=%#v", binding.GetLabels(), binding.GetOwnerReferences())
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("list created UID-fenced role: err=%v", err)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("list performed an RBAC migration write: %#v", action)
		}
	}
}

func TestAppAccessGrantBrokerListsLegacyApprovalsWithoutUpdatePermission(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	handler := brokerTestHandler(client, true)
	handler.sarClient = func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
		kube := kubefake.NewSimpleClientset()
		kube.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
			review.Status.Allowed = review.Spec.ResourceAttributes.Verb == "get"
			return true, review, nil
		})
		return kube.AuthorizationV1().SubjectAccessReviews(), nil
	}
	before := len(client.Actions())
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", brokerListCall()))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var result appAccessGrantResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].User != "member" || !result.Items[0].MigrationPending {
		t.Fatalf("read-only legacy approval list = %#v", result.Items)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.GetOwnerReferences()) != 0 || len(role.Object["rules"].([]any)) != 2 || role.GetLabels()[appAccessInstanceUID] != "" {
		t.Fatalf("list without update permission mutated legacy role: labels=%#v owners=%#v rules=%#v", role.GetLabels(), role.GetOwnerReferences(), role.Object["rules"])
	}
	binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.GetOwnerReferences()) != 0 || binding.GetLabels()[appAccessInstanceUID] != "" {
		t.Fatalf("list without update permission mutated legacy binding: labels=%#v owners=%#v", binding.GetLabels(), binding.GetOwnerReferences())
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("read-only list created the UID-fenced role: err=%v", err)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("read-only list performed mutation action: %#v", action)
		}
	}
}

func TestAppAccessGrantBrokerListsWhenMigrationStatusReadsFail(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	client.PrependReactor("get", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("RBAC read temporarily unavailable")
	})
	before := len(client.Actions())
	response := httptest.NewRecorder()
	brokerTestHandler(client, true).Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", brokerListCall()))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var result appAccessGrantResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !result.Items[0].MigrationPending {
		t.Fatalf("status-read failure hint = %#v", result.Items)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("migration status probe attempted a write: %#v", action)
		}
	}
}

func TestAppAccessGrantBrokerListsCurrentGrantsWithoutRewritingThem(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	handler := brokerTestHandler(client, true)
	createCall := brokerListCall()
	createCall.AppAccessOperation = "create"
	createCall.AppAccessUser = "member"
	created := httptest.NewRecorder()
	handler.Handle(created, brokerTestRequest(brokerCreateBody(brokerTestProjectUID, brokerTestInstanceUID, "member"), "org", "workspace", createCall))
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", created.Code, http.StatusOK, created.Body.String())
	}
	before := len(client.Actions())
	var verbs []string
	handler.sarClient = func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
		kube := kubefake.NewSimpleClientset()
		kube.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
			verb := review.Spec.ResourceAttributes.Verb
			verbs = append(verbs, verb)
			review.Status.Allowed = verb == "get"
			return true, review, nil
		})
		return kube.AuthorizationV1().SubjectAccessReviews(), nil
	}
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", brokerListCall()))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var result appAccessGrantResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].User != "member" || result.Items[0].MigrationPending {
		t.Fatalf("current approval list = %#v", result.Items)
	}
	if len(verbs) != 2 || verbs[0] != "get" || verbs[1] != "get" {
		t.Fatalf("SAR verbs = %#v, want only the two target get checks", verbs)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("listing current grants rewrote RBAC: %#v", action)
		}
	}
}

func TestAppAccessGrantBrokerDoesNotCarryApprovalsAcrossProjectUID(t *testing.T) {
	const replacementProjectUID = "replacement-project-uid"
	const replacementInstanceUID = brokerTestInstanceUID
	client := brokerTestClientFor(replacementProjectUID, replacementInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "former-member", "railgrid:former@example.test")
	brokerSeedIdentityGrant(t, client, appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
	}, "former-member", "railgrid:former@example.test")
	handler := brokerTestHandler(client, true)
	call := brokerListCall()
	call.AppAccessOperation = "create"
	call.AppAccessUser = "new-member"
	call.AppAccessProjectUID = replacementProjectUID
	call.AppAccessUID = replacementInstanceUID
	body := brokerCreateBody(replacementProjectUID, replacementInstanceUID, "new-member")
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", call))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{}); err != nil {
		t.Fatalf("old Project binding should remain attached to inert legacy role: %v", err)
	}
	currentBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(appAccessGrantRequest{Name: "app-demo-prod", UID: replacementInstanceUID, ProjectUID: replacementProjectUID}, "new-member", appAccessGrantSourcePublishing), metav1.GetOptions{})
	if err != nil || currentBinding.GetLabels()[appAccessProjectUID] != replacementProjectUID || currentBinding.GetLabels()[appAccessInstanceUID] != replacementInstanceUID {
		t.Fatalf("replacement binding = %#v err=%v", currentBinding, err)
	}
	legacyRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyRules, _, _ := unstructured.NestedSlice(legacyRole.Object, "rules")
	if len(legacyRules) != 0 || legacyRole.GetLabels()[appAccessProjectUID] != brokerTestProjectUID {
		t.Fatalf("old static role was not fenced without relabeling: labels=%#v rules=%#v", legacyRole.GetLabels(), legacyRules)
	}
	newRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessUIDRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: replacementInstanceUID, ProjectUID: replacementProjectUID,
	}), metav1.GetOptions{})
	if err != nil || newRole.GetLabels()[appAccessProjectUID] != replacementProjectUID || newRole.GetLabels()[appAccessInstanceUID] != replacementInstanceUID {
		t.Fatalf("replacement UID role = %#v err=%v", newRole, err)
	}
	oldIdentityRoleName := appAccessIdentityRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID})
	oldFence, oldFenceOK := identityAppAccessSubresource(brokerTestProjectUID, brokerTestInstanceUID)
	oldAuthenticationFence, oldAuthenticationOK := authenticationAppAccessSubresource(brokerTestProjectUID, brokerTestInstanceUID)
	newFence, newFenceOK := authenticationAppAccessSubresource(replacementProjectUID, replacementInstanceUID)
	if !oldFenceOK || !oldAuthenticationOK || !newFenceOK || oldAuthenticationFence == newFence || oldFence == newFence {
		t.Fatalf("Project UID replacement reused an authentication coordinate: oldAuth=%q oldLegacy=%q new=%q", oldAuthenticationFence, oldFence, newFence)
	}
	oldIdentityRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), oldIdentityRoleName, metav1.GetOptions{})
	if err != nil || oldIdentityRole.GetLabels()[appAccessProjectUID] != brokerTestProjectUID {
		t.Fatalf("old secondary UID role = %#v err=%v", oldIdentityRole, err)
	}
	oldRules, _, _ := unstructured.NestedSlice(oldIdentityRole.Object, "rules")
	oldResources, _, _ := unstructured.NestedStringSlice(oldRules[0].(map[string]any), "resources")
	if len(oldResources) != 1 || oldResources[0] != "instances/"+oldFence || brokerRoleHasResource(newRole, "instances/"+newFence) {
		t.Fatalf("replacement publication role inherited an old authentication permission: old=%#v new=%#v", oldRules, newRole.Object["rules"])
	}
	oldIdentityBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessIdentityBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, "former-member"), metav1.GetOptions{})
	if err != nil || !validRoleReference(oldIdentityBinding, oldIdentityRoleName) {
		t.Fatalf("old UID binding was rebound to replacement role: %#v err=%v", oldIdentityBinding, err)
	}
	if brokerSARAllowed(client, "railgrid:former@example.test", "infrastructure.railgrid.ai", "instances/"+newFence, "app-demo-prod", AccessVerb) {
		t.Fatal("retained old secondary grant authorized the replacement Project's identity SAR")
	}
	oldPrimary, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{})
	if err != nil || !validRoleReference(oldPrimary, appAccessRoleName("app-demo-prod")) {
		t.Fatalf("old managed primary binding should remain attached to the inert legacy role: %#v err=%v", oldPrimary, err)
	}
}

func TestAuthenticationAppAccessSubresourceFencesBothUIDs(t *testing.T) {
	base, ok := authenticationAppAccessSubresource("project-one", "instance-one")
	if !ok || !strings.HasPrefix(base, AccessSubresource+".auth.") {
		t.Fatalf("authentication fence = %q, valid=%v", base, ok)
	}
	if same, valid := authenticationAppAccessSubresource("project-one", "instance-one"); !valid || same != base {
		t.Fatalf("same Project+Instance produced different fence: %q valid=%v", same, valid)
	}
	for _, pair := range [][2]string{{"project-two", "instance-one"}, {"project-one", "instance-two"}} {
		got, valid := authenticationAppAccessSubresource(pair[0], pair[1])
		if !valid || got == base {
			t.Fatalf("changed UID pair %q/%q reused fence %q valid=%v", pair[0], pair[1], got, valid)
		}
	}
	for _, pair := range [][2]string{{"", "instance-one"}, {"project-one", "bad/instance"}} {
		if got, valid := authenticationAppAccessSubresource(pair[0], pair[1]); valid || got != "" {
			t.Fatalf("malformed UID pair %q/%q accepted as %q", pair[0], pair[1], got)
		}
	}
	legacy, valid := identityAppAccessSubresource("project-one", "instance-one")
	if !valid || legacy == base {
		t.Fatalf("authentication SAR coordinate overlaps the legacy secondary identity permission: auth=%q legacy=%q", base, legacy)
	}
}

func TestAppAccessGeneratedRBACNamesFitKubernetesLimits(t *testing.T) {
	longName := "a" + strings.Repeat("b", 251) + "c"
	req := appAccessGrantRequest{Name: longName, ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID}
	if got := appAccessUIDRoleName(req); len(got) > 253 {
		t.Fatalf("UID role name has %d characters, limit is 253", len(got))
	}
	if got := appAccessBindingNameForSource(longName, strings.Repeat("u", 253), appAccessGrantSourceAuthentication); len(got) > 253 {
		t.Fatalf("authentication binding name has %d characters, limit is 253", len(got))
	}
	if got := appAccessBindingNameForSource(longName, strings.Repeat("u", 253), appAccessGrantSourcePublishing); len(got) > 253 {
		t.Fatalf("publishing binding name has %d characters, limit is 253", len(got))
	}
	longReq := appAccessGrantRequest{Name: longName, UID: strings.Repeat("i", 253), ProjectUID: strings.Repeat("p", 253)}
	for _, source := range []string{appAccessGrantSourcePublishing, appAccessGrantSourceAuthentication} {
		if got := appAccessUIDBindingName(longReq, strings.Repeat("u", 253), source); len(got) > 253 {
			t.Fatalf("%s UID-qualified binding name has %d characters, limit is 253", source, len(got))
		}
	}
}

func TestAppAccessGrantBrokerFencesStaleUIDRoleBeforeCreatingReplacementGrant(t *testing.T) {
	const (
		projectUID  = "replacement-project-uid"
		instanceUID = "replacement-instance-uid"
	)
	client := brokerTestClientFor(projectUID, instanceUID)
	oldProject := brokerTestProject(client).DeepCopy()
	oldProject.SetUID(types.UID("old-project-uid"))
	oldInstance := brokerTestInstance(client).DeepCopy()
	oldInstance.SetUID(types.UID("old-instance-uid"))
	oldInstance.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: oldProject.GetName(), UID: oldProject.GetUID(),
	}})
	oldReq := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: oldProject.GetName(), ProjectUID: string(oldProject.GetUID()), UID: string(oldInstance.GetUID()),
	}
	staleRole := newAppAccessRole(oldProject, oldInstance, oldReq)
	staleRole.SetUID(types.UID("stale-access-role-uid"))
	staleRole.SetResourceVersion("47")
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), staleRole, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	staleBinding := newAppAccessBinding(oldProject, oldInstance, oldReq, IdentityAccount{
		User: "former-member", RBACIdentity: "railgrid:former@example.test",
	}, appAccessUIDRoleName(oldReq), appAccessGrantSourcePublishing)
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), staleBinding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	handler := brokerTestHandler(client, true)
	call := brokerListCall()
	call.AppAccessOperation = "create"
	call.AppAccessUser = "new-member"
	call.AppAccessUID = instanceUID
	call.AppAccessProjectUID = projectUID
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(brokerCreateBody(projectUID, instanceUID, "new-member"), "org", "workspace", call))
	if response.Code != http.StatusOK {
		t.Fatalf("replacement grant create status=%d body=%s", response.Code, response.Body.String())
	}
	persistedStaleRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), staleRole.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	staleRules, _, _ := unstructured.NestedSlice(persistedStaleRole.Object, "rules")
	if len(staleRules) != 0 || persistedStaleRole.GetUID() != staleRole.GetUID() {
		t.Fatalf("interactive create left stale reachability active: uid=%q rules=%#v", persistedStaleRole.GetUID(), staleRules)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), staleBinding.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatalf("old binding should remain attached to its now-inert role: %v", err)
	}
	currentReq := appAccessGrantRequest{Name: "app-demo-prod", UID: instanceUID, ProjectUID: projectUID}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(currentReq, "new-member", appAccessGrantSourcePublishing), metav1.GetOptions{}); err != nil {
		t.Fatalf("replacement approval was not created: %v", err)
	}
}

func TestAppAccessGrantBrokerRejectsAggregatedManagedRoles(t *testing.T) {
	t.Run("legacy role cannot migrate", func(t *testing.T) {
		client := brokerTestClient(brokerTestInstanceUID)
		brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
		role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role.Object["aggregationRule"] = map[string]any{"clusterRoleSelectors": []any{map[string]any{}}}
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		req := appAccessGrantRequest{
			Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
			Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
		}
		if recognizedAppAccessRole(role, req) {
			t.Fatal("ClusterRole with aggregationRule was recognized for migration")
		}
		before := len(client.Actions())
		reconciler := appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}
		if err := reconciler.reconcileInstance(context.Background(), "cluster-id", "app-demo-prod"); err != nil {
			t.Fatalf("unrecognized aggregated role should remain untouched without blocking the controller: %v", err)
		}
		persisted, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), role.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, found := persisted.Object["aggregationRule"]; !found {
			t.Fatal("rejected legacy role was rewritten during migration")
		}
		assertNoRBACWrites(t, client.Actions()[before:])
	})
	t.Run("authentication primary role cannot be adopted", func(t *testing.T) {
		client := brokerTestClient(brokerTestInstanceUID)
		req := appAccessGrantRequest{
			Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
			Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
		}
		project, instance, err := brokerTestHandler(client, true).loadOwnedTarget(context.Background(), client, req)
		if err != nil {
			t.Fatal(err)
		}
		roleName := appAccessUIDRoleNameForSource(req, appAccessGrantSourceAuthentication)
		role := newAppAccessRoleForSource(project, instance, req, appAccessGrantSourceAuthentication)
		if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), role, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		role, err = client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role.Object["aggregationRule"] = map[string]any{"clusterRoleSelectors": []any{map[string]any{}}}
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if recognizedAppAccessRoleForSource(role, req, appAccessGrantSourceAuthentication) {
			t.Fatal("authentication ClusterRole with aggregationRule was recognized for adoption")
		}
		call := brokerListCall()
		call.AppAccessOperation = "create"
		call.AppAccessUser = "member"
		call.AppAccessSourceVerb = "authentication-grants"
		response := httptest.NewRecorder()
		brokerTestHandler(client, true).Handle(response, brokerTestRequest(brokerCreateBody(brokerTestProjectUID, brokerTestInstanceUID, "member"), "org", "workspace", call))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("create status = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
		}
		persisted, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, found := persisted.Object["aggregationRule"]; !found {
			t.Fatal("rejected authentication role was rewritten during grant creation")
		}
	})
}

func TestDeleteGrantObjectUsesObjectPreconditions(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "grant", "uid": "grant-uid", "resourceVersion": "17"},
	}}
	_ = deleteGrantObjectWithPreconditions(context.Background(), client.Resource(appAccessBindingGVR), obj)

	actions := client.Actions()
	if len(actions) != 1 {
		t.Fatalf("delete actions = %#v, want one delete", actions)
	}
	delete, ok := actions[0].(k8stesting.DeleteAction)
	if !ok {
		t.Fatalf("delete action type = %T, want DeleteActionWithOptions", actions[0])
	}
	preconditions := delete.GetDeleteOptions().Preconditions
	if preconditions == nil || preconditions.UID == nil || string(*preconditions.UID) != "grant-uid" ||
		preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "17" {
		t.Fatalf("delete preconditions = %#v, want UID and resourceVersion fencing", preconditions)
	}
}

func TestAppAccessGrantBrokerKeepsProductionAuthAppOnly(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	handler := brokerTestHandler(client, true)
	membershipCalls := 0
	handler.memberships = tenant.MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		membershipCalls++
		return nil, errors.New("production app access does not require workspace membership")
	})
	call := brokerListCall()
	call.AppAccessOperation = "create"
	call.AppAccessUser = "member"
	call.AppAccessSourceVerb = "authentication-grants"
	body := brokerCreateBody(brokerTestProjectUID, brokerTestInstanceUID, "member")
	response := httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", call))
	if response.Code != http.StatusOK || membershipCalls != 0 {
		t.Fatalf("status=%d membership lookups=%d body=%s", response.Code, membershipCalls, response.Body.String())
	}
	roles, err := client.Resource(appAccessClusterRoleGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(roles.Items) != 1 {
		t.Fatalf("production app role=%#v err=%v", roles.Items, err)
	}
	req := appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID, Group: "infrastructure.railgrid.ai", Resource: "instances"}
	roleName := appAccessUIDRoleNameForSource(req, appAccessGrantSourceAuthentication)
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("authentication primary role missing: %v", err)
	}
	authFence, ok := authenticationAppAccessSubresource(req.ProjectUID, req.UID)
	if !ok || !brokerRoleHasResource(role, "instances/"+authFence) {
		t.Fatalf("authentication primary role does not carry its UID-fenced identity permission: rules=%#v", role.Object["rules"])
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(req), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("authentication grant created an independently-authorizing secondary role: %v", err)
	}
	bindings, err := client.Resource(appAccessBindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(bindings.Items) != 1 || !validRoleReference(&bindings.Items[0], roleName) {
		t.Fatalf("authentication primary binding=%#v err=%v", bindings.Items, err)
	}
	if !brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+authFence, req.Name, AccessVerb) {
		t.Fatal("active authentication approval did not authorize its UID-fenced SAR coordinate")
	}
}

func TestAppAccessGrantSourcesRemainIndependentAndRevokePrimaryFirst(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	handler := brokerTestHandler(client, true)
	createSource := func(source string) *httptest.ResponseRecorder {
		call := brokerListCall()
		call.AppAccessOperation = "create"
		call.AppAccessUser = "member"
		if source == appAccessGrantSourceAuthentication {
			call.AppAccessSourceVerb = "authentication-grants"
		}
		response := httptest.NewRecorder()
		handler.Handle(response, brokerTestRequest(brokerCreateBody(brokerTestProjectUID, brokerTestInstanceUID, "member"), "org", "workspace", call))
		return response
	}
	req := appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID, Group: "infrastructure.railgrid.ai", Resource: "instances"}
	authFence, ok := authenticationAppAccessSubresource(req.ProjectUID, req.UID)
	if !ok {
		t.Fatal("test UIDs did not produce an authentication fence")
	}
	if response := createSource(appAccessGrantSourcePublishing); response.Code != http.StatusOK {
		t.Fatalf("publishing create status=%d body=%s", response.Code, response.Body.String())
	}
	if brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+authFence, req.Name, AccessVerb) {
		t.Fatal("publishing-only grant authorized an authentication SAR")
	}
	if response := createSource(appAccessGrantSourceAuthentication); response.Code != http.StatusOK {
		t.Fatalf("authentication create status=%d body=%s", response.Code, response.Body.String())
	}
	if !brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+authFence, req.Name, AccessVerb) {
		t.Fatal("authentication grant did not authorize its SAR while active")
	}
	publishingName := appAccessUIDBindingName(req, "member", appAccessGrantSourcePublishing)
	authenticationName := appAccessUIDBindingName(req, "member", appAccessGrantSourceAuthentication)
	publishingRoleName := appAccessUIDRoleNameForSource(req, appAccessGrantSourcePublishing)
	authenticationRoleName := appAccessUIDRoleNameForSource(req, appAccessGrantSourceAuthentication)
	if publishingRoleName == authenticationRoleName {
		t.Fatal("publishing and authentication grants share a primary role")
	}
	for name, roleName := range map[string]string{publishingName: publishingRoleName, authenticationName: authenticationRoleName} {
		binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil || !validRoleReference(binding, roleName) {
			t.Fatalf("primary binding %q=%#v err=%v; want source-specific role %q", name, binding, err, roleName)
		}
	}
	publishingRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), publishingRoleName, metav1.GetOptions{})
	if err != nil || brokerRoleHasResource(publishingRole, "instances/"+authFence) {
		t.Fatalf("publishing role unexpectedly carries authentication permission: role=%#v err=%v", publishingRole, err)
	}
	authenticationRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), authenticationRoleName, metav1.GetOptions{})
	if err != nil || !brokerRoleHasResource(authenticationRole, "instances/"+authFence) {
		t.Fatalf("authentication role lacks its UID permission: role=%#v err=%v", authenticationRole, err)
	}

	for _, source := range []string{appAccessGrantSourcePublishing, appAccessGrantSourceAuthentication} {
		call := brokerListCall()
		if source == appAccessGrantSourceAuthentication {
			call.AppAccessSourceVerb = "authentication-grants"
		}
		response := httptest.NewRecorder()
		handler.Handle(response, brokerTestRequest(brokerListBody(), "org", "workspace", call))
		var result appAccessGrantResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(result.Items) != 1 || result.Items[0].Name != appAccessUIDBindingName(req, "member", source) {
			t.Fatalf("%s listing status=%d items=%#v body=%s", source, response.Code, result.Items, response.Body.String())
		}
	}

	reset := brokerListCall()
	reset.AppAccessOperation = "reset"
	response := httptest.NewRecorder()
	resetBody := strings.Replace(brokerListBody(), `"operation":"list"`, `"operation":"reset"`, 1)
	handler.Handle(response, brokerTestRequest(resetBody, "org", "workspace", reset))
	if response.Code != http.StatusOK {
		t.Fatalf("publishing reset status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), publishingName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("publishing primary binding survived its reset: %v", err)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), authenticationName, metav1.GetOptions{}); err != nil {
		t.Fatalf("publishing reset removed authentication grant: %v", err)
	}
	if !brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+authFence, req.Name, AccessVerb) {
		t.Fatal("publishing reset removed the independent authentication SAR authorization")
	}
	if response := createSource(appAccessGrantSourcePublishing); response.Code != http.StatusOK {
		t.Fatalf("publishing regrant status=%d body=%s", response.Code, response.Body.String())
	}

	// A stale secondary identity binding can drift and resist cleanup. The
	// authentication primary role is the authorization boundary, so deleting
	// its binding must deny fresh assertions even if that stale object remains.
	brokerSeedIdentityGrantForSource(t, client, req, "member", "railgrid:member@example.test", appAccessGrantSourceAuthentication)
	legacyIdentityName := appAccessIdentityBindingNameForSource(req, "member", appAccessGrantSourceAuthentication)
	drifted, err := client.Resource(appAccessBindingGVR).Get(context.Background(), legacyIdentityName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	drifted.Object["roleRef"] = map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "foreign-role"}
	if _, err := client.Resource(appAccessBindingGVR).Update(context.Background(), drifted, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	primary, err := client.Resource(appAccessBindingGVR).Get(context.Background(), authenticationName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	primary.SetUID(types.UID("authentication-primary-uid"))
	primary.SetResourceVersion("33")
	if _, err := client.Resource(appAccessBindingGVR).Update(context.Background(), primary, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	conflictOnce := true
	client.PrependReactor("delete", "clusterrolebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflictOnce && action.(k8stesting.DeleteAction).GetName() == authenticationName {
			conflictOnce = false
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: appAccessBindingGVR.Group, Resource: appAccessBindingGVR.Resource}, authenticationName, errors.New("resourceVersion conflict"))
		}
		return false, nil, nil
	})
	beforeRevoke := len(client.Actions())
	revoke := brokerListCall()
	revoke.AppAccessOperation = "revoke"
	revoke.AppAccessSourceVerb = "authentication-grants"
	revoke.AppAccessGrant = authenticationName
	body := `{"operation":"revoke","group":"infrastructure.railgrid.ai","resource":"instances","name":"app-demo-prod","uid":"` + brokerTestInstanceUID + `","projectName":"demo","projectUID":"` + brokerTestProjectUID + `","grant":"` + authenticationName + `"}`
	response = httptest.NewRecorder()
	handler.Handle(response, brokerTestRequest(body, "org", "workspace", revoke))
	if response.Code != http.StatusOK {
		t.Fatalf("authentication revoke status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), authenticationName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("identity cleanup drift preserved primary authentication grant: %v", err)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), legacyIdentityName, metav1.GetOptions{}); err != nil {
		t.Fatalf("drifted secondary identity binding should remain for cleanup: %v", err)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), publishingName, metav1.GetOptions{}); err != nil {
		t.Fatalf("authentication revoke removed the independent publishing grant: %v", err)
	}
	if brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+authFence, req.Name, AccessVerb) {
		t.Fatal("revoked authentication primary binding still authorizes a fresh identity SAR")
	}
	if !brokerSARAllowed(client, "railgrid:member@example.test", req.Group, req.Resource+"/"+AccessSubresource, req.Name, AccessVerb) {
		t.Fatal("authentication revoke removed the independent publishing reachability grant")
	}
	primaryDeleteAttempts := 0
	for _, action := range client.Actions()[beforeRevoke:] {
		if action.GetVerb() == "delete" && action.GetResource().Resource == appAccessBindingGVR.Resource && action.(k8stesting.DeleteAction).GetName() == authenticationName {
			primaryDeleteAttempts++
		}
	}
	if primaryDeleteAttempts != 2 {
		t.Fatalf("primary delete attempts after one conflict = %d, want bounded retry once", primaryDeleteAttempts)
	}
}

func brokerListCall() tenant.DelegatedCall {
	return tenant.DelegatedCall{
		User: "owner", RBACIdentity: "railgrid:owner@example.test", Provider: "app-studio",
		Capability: appAccessCapability, Scope: "workspace", ActionProof: true,
		AppAccessOperation: "list", AppAccessGroup: "infrastructure.railgrid.ai", AppAccessResource: "instances",
		AppAccessName: "app-demo-prod", AppAccessUID: brokerTestInstanceUID,
		AppAccessProject: "demo", AppAccessProjectUID: brokerTestProjectUID,
		AppAccessSourceVerb: "publishing-grants", AppAccessSourceMethod: http.MethodGet,
	}
}

func brokerListBody() string {
	return `{"operation":"list","group":"infrastructure.railgrid.ai","resource":"instances","name":"app-demo-prod","uid":"` + brokerTestInstanceUID + `","projectName":"demo","projectUID":"` + brokerTestProjectUID + `"}`
}

func brokerCreateBody(projectUID, instanceUID, user string) string {
	return `{"operation":"create","group":"infrastructure.railgrid.ai","resource":"instances","name":"app-demo-prod","uid":"` + instanceUID + `","projectName":"demo","projectUID":"` + projectUID + `","user":"` + user + `"}`
}

func brokerSeedLegacyGrant(t *testing.T, client *dynamicfake.FakeDynamicClient, projectUID, user, identity string) {
	t.Helper()
	req := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: "demo", ProjectUID: projectUID,
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": appAccessRoleName(req.Name), "uid": "legacy-access-role-uid", "resourceVersion": "17", "labels": map[string]any{
			appAccessLabel: req.Name, appAccessProject: req.Project, appAccessProjectUID: projectUID,
		}},
		"rules": appAccessRoleRules(req),
	}}
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy role: %v", err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessBindingName(req.Name, user), "uid": "legacy-access-binding-uid", "resourceVersion": "18", "labels": map[string]any{
			appAccessLabel: req.Name, appAccessProject: req.Project, appAccessProjectUID: projectUID, appAccessUserLabel: user,
		}},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": identity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessRoleName(req.Name)},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy binding: %v", err)
	}
}

func brokerSeedIdentityGrant(t *testing.T, client *dynamicfake.FakeDynamicClient, req appAccessGrantRequest, user, identity string) {
	brokerSeedIdentityGrantForSource(t, client, req, user, identity, appAccessGrantSourcePublishing)
}

func brokerSeedIdentityGrantForSource(t *testing.T, client *dynamicfake.FakeDynamicClient, req appAccessGrantRequest, user, identity, source string) {
	t.Helper()
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": req.Project, "uid": req.ProjectUID},
	}}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
		"metadata": map[string]any{"name": req.Name, "uid": req.UID},
	}}
	role := newAppAccessIdentityRole(project, instance, req)
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed old UID-fenced role: %v", err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessIdentityBindingNameForSource(req, user, source), "labels": map[string]any{
			appAccessLabel: req.Name, appAccessProject: req.Project, appAccessProjectUID: req.ProjectUID,
			appAccessInstanceUID: req.UID, appAccessUserLabel: user, appAccessSourceLabel: source,
		}},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": identity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessIdentityRoleName(req)},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed old UID-fenced binding: %v", err)
	}
}

func brokerRoleHasResource(role *unstructured.Unstructured, resource string) bool {
	if role == nil {
		return false
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	for _, rule := range rules {
		fields, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		resources, _, _ := unstructured.NestedStringSlice(fields, "resources")
		for _, candidate := range resources {
			if candidate == resource {
				return true
			}
		}
	}
	return false
}

// brokerSARAllowed evaluates the exact named resource permission shape used
// by app-access grants, following ClusterRoleBinding subjects and roleRefs.
func brokerSARAllowed(client dynamic.Interface, user, group, resource, name, verb string) bool {
	ctx := context.Background()
	roles, err := client.Resource(appAccessClusterRoleGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	roleByName := make(map[string]*unstructured.Unstructured, len(roles.Items))
	for i := range roles.Items {
		role := &roles.Items[i]
		roleByName[role.GetName()] = role
	}
	bindings, err := client.Resource(appAccessBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
		bound := false
		for _, raw := range subjects {
			subject, ok := raw.(map[string]any)
			if ok && subject["kind"] == "User" && subject["name"] == user {
				bound = true
				break
			}
		}
		if !bound {
			continue
		}
		roleName, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		role := roleByName[roleName]
		if role == nil {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
			resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
			resourceNames, _, _ := unstructured.NestedStringSlice(rule, "resourceNames")
			verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
			if containsGrantValue(groups, group) && containsGrantValue(resources, resource) &&
				containsGrantValue(resourceNames, name) && containsGrantValue(verbs, verb) {
				return true
			}
		}
	}
	return false
}

func containsGrantValue(values []string, expected string) bool {
	for _, value := range values {
		if value == expected || value == "*" {
			return true
		}
	}
	return false
}

func brokerTestRequest(body, org, workspace string, call tenant.DelegatedCall) *http.Request {
	ctx := tenant.WithContext(context.Background(), tenant.TenantContext{User: "owner", OrgUUID: "org", WorkspaceUUID: "workspace"})
	ctx = tenant.WithDelegatedCall(ctx, call)
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/"+org+"/workspaces/"+workspace+"/app-auth/grants", strings.NewReader(body))
	req = req.WithContext(ctx)
	return mux.SetURLVars(req, map[string]string{"org": org, "workspace": workspace})
}

func brokerTestHandler(client *dynamicfake.FakeDynamicClient, allowed bool) *AppAccessGrantHandler {
	return &AppAccessGrantHandler{
		clients: func(string) (dynamic.Interface, error) { return client, nil },
		sarClient: func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
			kube := kubefake.NewSimpleClientset()
			kube.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
				review.Status.Allowed = allowed
				return true, review, nil
			})
			return kube.AuthorizationV1().SubjectAccessReviews(), nil
		},
		identityApp: func(context.Context, InstanceRef) (IdentityApp, error) {
			return IdentityApp{InstanceUID: brokerTestInstanceUID, AppID: brokerTestProjectUID + "/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: true}, nil
		},
		workspaceCluster: func(context.Context, string, string) (string, error) { return "cluster-id", nil },
		accountLookup: func(_ context.Context, user string) (IdentityAccount, error) {
			return IdentityAccount{User: user, Email: user + "@example.test", RBACIdentity: "railgrid:" + user + "@example.test"}, nil
		},
		memberships: tenant.MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			return &tenancyv1alpha1.UserMembershipIndex{Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", WorkspaceUUID: "workspace"}}}}, nil
		}),
	}
}

func brokerTestClient(instanceUID string) *dynamicfake.FakeDynamicClient {
	return brokerTestClientFor(brokerTestProjectUID, instanceUID)
}

func brokerTestClientFor(projectUID, instanceUID string) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "demo", "uid": projectUID},
		"spec": map[string]any{
			"environments": []any{
				map[string]any{
					"name": "production",
					"bindings": []any{
						map[string]any{
							"name": "prod", "provider": "app-studio", "kind": "providerResource",
							"resourceRef": map[string]any{
								"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance", "resource": "instances", "name": "app-demo-prod",
							},
						},
					},
				},
			},
		},
	}}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
		"metadata": map[string]any{
			"name": "app-demo-prod", "uid": instanceUID,
			"labels":          map[string]any{appAccessProject: "demo"},
			"ownerReferences": []any{map[string]any{"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project", "name": "demo", "uid": projectUID}},
		},
	}}
	listKinds := map[schema.GroupVersionResource]string{
		appAccessProjectGVR: "ProjectList",
		{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}: "InstanceList",
		appAccessClusterRoleGVR: "ClusterRoleList",
		appAccessBindingGVR:     "ClusterRoleBindingList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, project, instance)
}
