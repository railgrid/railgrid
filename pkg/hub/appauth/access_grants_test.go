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
	if len(roles.Items) != 2 || len(bindings.Items) != 2 {
		t.Fatalf("RBAC objects = roles:%d bindings:%d, want legacy and UID-fenced pairs", len(roles.Items), len(bindings.Items))
	}
	legacyBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := bindingSubjectUser(legacyBinding); got != "railgrid:member@example.test" {
		t.Fatalf("created grant subject = %q, want current account RBAC identity", got)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
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
	identityRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identityRules, _, _ := unstructured.NestedSlice(identityRole.Object, "rules")
	if len(identityRules) != 1 {
		t.Fatalf("UID-fenced identity role rules = %#v", identityRules)
	}
	uidResources, _, _ := unstructured.NestedStringSlice(identityRules[0].(map[string]any), "resources")
	identityFence, ok := identityAppAccessSubresource(brokerTestProjectUID, brokerTestInstanceUID)
	if !ok || len(uidResources) != 1 || uidResources[0] != "instances/"+identityFence {
		t.Fatalf("UID-fenced identity role rules = %#v", identityRules)
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
	identityBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessIdentityBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, "member"), metav1.GetOptions{})
	if err != nil || bindingSubjectUser(identityBinding) != "railgrid:member@example.test" {
		t.Fatalf("UID-fenced grant binding = %#v err=%v", identityBinding, err)
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
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old Project binding survived replacement cleanup: err=%v", err)
	}
	currentLegacyBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "new-member"), metav1.GetOptions{})
	if err != nil || currentLegacyBinding.GetLabels()[appAccessProjectUID] != replacementProjectUID || currentLegacyBinding.GetLabels()[appAccessInstanceUID] != replacementInstanceUID {
		t.Fatalf("replacement legacy binding = %#v err=%v", currentLegacyBinding, err)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role.GetLabels()[appAccessProjectUID] != replacementProjectUID || role.GetLabels()[appAccessInstanceUID] != replacementInstanceUID {
		t.Fatalf("role was carried across Project UID: %#v", role.GetLabels())
	}
	oldIdentityRoleName := appAccessIdentityRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID})
	newIdentityRoleName := appAccessIdentityRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: replacementInstanceUID, ProjectUID: replacementProjectUID})
	if oldIdentityRoleName == newIdentityRoleName {
		t.Fatal("Project replacement reused the UID-fenced role name")
	}
	oldFence, oldFenceOK := identityAppAccessSubresource(brokerTestProjectUID, brokerTestInstanceUID)
	newFence, newFenceOK := identityAppAccessSubresource(replacementProjectUID, replacementInstanceUID)
	if !oldFenceOK || !newFenceOK || oldFence == newFence {
		t.Fatalf("Project replacement reused the UID-fenced SAR coordinate: old=%q new=%q", oldFence, newFence)
	}
	oldIdentityRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), oldIdentityRoleName, metav1.GetOptions{})
	if err != nil || oldIdentityRole.GetLabels()[appAccessProjectUID] != brokerTestProjectUID {
		t.Fatalf("old UID role = %#v err=%v", oldIdentityRole, err)
	}
	newIdentityRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), newIdentityRoleName, metav1.GetOptions{})
	if err != nil || newIdentityRole.GetLabels()[appAccessProjectUID] != replacementProjectUID || newIdentityRole.GetLabels()[appAccessInstanceUID] != replacementInstanceUID {
		t.Fatalf("replacement UID role = %#v err=%v", newIdentityRole, err)
	}
	oldRules, _, _ := unstructured.NestedSlice(oldIdentityRole.Object, "rules")
	oldResources, _, _ := unstructured.NestedStringSlice(oldRules[0].(map[string]any), "resources")
	newRules, _, _ := unstructured.NestedSlice(newIdentityRole.Object, "rules")
	newResources, _, _ := unstructured.NestedStringSlice(newRules[0].(map[string]any), "resources")
	if len(oldResources) != 1 || oldResources[0] != "instances/"+oldFence || len(newResources) != 1 || newResources[0] != "instances/"+newFence {
		t.Fatalf("Project replacement identity rules old=%#v new=%#v", oldRules, newRules)
	}
	oldIdentityBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessIdentityBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, "former-member"), metav1.GetOptions{})
	if err != nil || !validRoleReference(oldIdentityBinding, oldIdentityRoleName) {
		t.Fatalf("old UID binding was rebound to replacement role: %#v err=%v", oldIdentityBinding, err)
	}
}

func TestIdentityAppAccessSubresourceFencesBothUIDs(t *testing.T) {
	base, ok := identityAppAccessSubresource("project-one", "instance-one")
	if !ok || !strings.HasPrefix(base, AccessSubresource+".") {
		t.Fatalf("base identity fence = %q, valid=%v", base, ok)
	}
	if same, valid := identityAppAccessSubresource("project-one", "instance-one"); !valid || same != base {
		t.Fatalf("same Project+Instance produced different fence: %q valid=%v", same, valid)
	}
	for _, pair := range [][2]string{{"project-two", "instance-one"}, {"project-one", "instance-two"}} {
		got, valid := identityAppAccessSubresource(pair[0], pair[1])
		if !valid || got == base {
			t.Fatalf("changed UID pair %q/%q reused fence %q valid=%v", pair[0], pair[1], got, valid)
		}
	}
	for _, pair := range [][2]string{{"", "instance-one"}, {"project-one", "bad/instance"}} {
		if got, valid := identityAppAccessSubresource(pair[0], pair[1]); valid || got != "" {
			t.Fatalf("malformed UID pair %q/%q accepted as %q", pair[0], pair[1], got)
		}
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
		req := appAccessGrantRequest{Name: "app-demo-prod", ProjectUID: brokerTestProjectUID}
		if recognizedAppAccessRole(role, req) {
			t.Fatal("ClusterRole with aggregationRule was recognized for migration")
		}
		reconciler := appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}
		if err := reconciler.reconcileInstance(context.Background(), "cluster-id", "app-demo-prod"); err == nil {
			t.Fatal("background migration accepted a ClusterRole with aggregationRule")
		}
		persisted, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), role.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, found := persisted.Object["aggregationRule"]; !found {
			t.Fatal("rejected legacy role was rewritten during migration")
		}
	})
	t.Run("identity role cannot be adopted", func(t *testing.T) {
		client := brokerTestClient(brokerTestInstanceUID)
		req := appAccessGrantRequest{
			Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
			Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
		}
		brokerSeedIdentityGrant(t, client, req, "member", "railgrid:member@example.test")
		roleName := appAccessIdentityRoleName(req)
		role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role.Object["aggregationRule"] = map[string]any{"clusterRoleSelectors": []any{map[string]any{}}}
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		project, instance, err := brokerTestHandler(client, true).loadOwnedTarget(context.Background(), client, req)
		if err != nil {
			t.Fatal(err)
		}
		if recognizedAppAccessIdentityRole(role, project, instance, req) {
			t.Fatal("ClusterRole with aggregationRule was recognized for adoption")
		}
		call := brokerListCall()
		call.AppAccessOperation = "create"
		call.AppAccessUser = "member"
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
			t.Fatal("rejected identity role was rewritten during grant creation")
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
	if err != nil || len(roles.Items) != 2 {
		t.Fatalf("production app role=%#v err=%v", roles.Items, err)
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{}); err != nil {
		t.Fatalf("production app has no UID-fenced role: %v", err)
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
		"metadata": map[string]any{"name": appAccessRoleName(req.Name), "labels": map[string]any{
			appAccessLabel: req.Name, appAccessProject: req.Project, appAccessProjectUID: projectUID,
		}},
		"rules": appAccessRoleRules(req),
	}}
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy role: %v", err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessBindingName(req.Name, user), "labels": map[string]any{
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
		"metadata": map[string]any{"name": appAccessIdentityBindingName(req, user), "labels": map[string]any{
			appAccessLabel: req.Name, appAccessProject: req.Project, appAccessProjectUID: req.ProjectUID,
			appAccessInstanceUID: req.UID, appAccessUserLabel: user,
		}},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": identity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessIdentityRoleName(req)},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed old UID-fenced binding: %v", err)
	}
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
