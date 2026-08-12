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

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

var (
	publishingTestTargetGVR = schema.GroupVersionResource{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications"}
	clusterRoleGVR          = clusterRoleResource.GVR
	clusterRoleBindingGVR   = clusterRoleBindingResource.GVR
)

func publishingTestDynamic(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR:     "ProjectList",
			publishingTestTargetGVR: "ApplicationList",
			clusterRoleGVR:          "ClusterRoleList",
			clusterRoleBindingGVR:   "ClusterRoleBindingList",
		},
		objects...,
	)
}

func publishingTestServer(t *testing.T, dyn *fake.FakeDynamicClient, members ...publishingMember) *mux.Router {
	t.Helper()
	client := asclient.NewFromDynamic(dyn)
	server := &Server{
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		publishingMembershipFetcher: func(context.Context, identity) ([]publishingMember, error) {
			return members, nil
		},
	}
	router := mux.NewRouter()
	server.Register(router)
	return router
}

func publishingTestProjectTyped(name, uid, access string) *aiv1alpha1.Project {
	values := map[string]any{"name": name + "-prod"}
	if access != "" {
		values["access"] = access
	}
	return &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Spec: aiv1alpha1.ProjectSpec{
			Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"},
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: projectProductionEnvironmentName,
				Mode: aiv1alpha1.ProjectEnvironmentModeArtifact,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name:     projectProductionBindingName,
					Provider: "app-studio",
					Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
						Name: name + "-prod", APIVersion: publishingTestTargetGVR.GroupVersion().String(), Kind: "Application", Resource: "applications",
					},
					Values: rawJSONForPublishing(values),
				}},
			}},
		},
	}
}

func publishingTestProject(name, uid, access string) *unstructured.Unstructured {
	object, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(publishingTestProjectTyped(name, uid, access))
	return &unstructured.Unstructured{Object: object}
}

func publishingTestTarget(name, uid, specAccess, url string) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": publishingTestTargetGVR.GroupVersion().String(), "kind": "Application",
		"metadata": map[string]any{"name": name, "uid": uid},
		"spec":     map[string]any{"access": specAccess},
		"status":   map[string]any{},
	}
	if url != "" {
		object["status"] = map[string]any{"url": url, "host": strings.TrimPrefix(url, "https://")}
	}
	return &unstructured.Unstructured{Object: object}
}

func rawJSONForPublishing(value any) runtime.RawExtension {
	raw, _ := json.Marshal(value)
	return runtime.RawExtension{Raw: raw}
}

func setPublishingIdentity(r *http.Request) {
	r.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:ws-1")
	r.Header.Set("X-Kedge-Cluster", "cluster-a")
	r.Header.Set("X-Kedge-User", "alice")
	r.Header.Set("Authorization", "Bearer test-token")
}

func publishingDo(t *testing.T, router *mux.Router, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	setPublishingIdentity(req)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRequestedAccessValueVocabulary(t *testing.T) {
	project := publishingTestProjectTyped("demo", "uid", "")
	for input, want := range map[string]string{
		"public":     accessPublic,
		"restricted": accessPrivate,
		"members":    accessPrivate,
		"private":    accessPrivate,
	} {
		got, err := requestedAccessValue(input, project)
		if err != nil || got != want {
			t.Fatalf("requestedAccessValue(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := requestedAccessValue("bogus", project); err == nil {
		t.Fatal("requestedAccessValue accepted an unknown mode")
	}
	// Empty preserves an explicit public setting, otherwise defaults private.
	publicProject := publishingTestProjectTyped("demo", "uid", accessPublic)
	if got, _ := requestedAccessValue("", publicProject); got != accessPublic {
		t.Fatalf("empty mode on public project = %q, want public", got)
	}
	if got, _ := requestedAccessValue("", project); got != accessPrivate {
		t.Fatalf("empty mode on unconfigured project = %q, want private", got)
	}
}

func TestPublishWritesAccessValueOntoProductionBinding(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", ""),
		publishingTestTarget("demo-prod", "runtime-uid-1", "public", "https://demo-prod-abc.apps.test"),
	)
	router := publishingTestServer(t, dyn)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing", `{"mode":"public"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", rec.Code, rec.Body.String())
	}
	var body projectPublishingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	if !body.Published || body.Publication == nil || body.Publication.Mode != "public" {
		t.Fatalf("publish response = %+v, want published public", body)
	}
	if !body.Publication.Ready || body.Publication.URL != "https://demo-prod-abc.apps.test" {
		t.Fatalf("publication view = %+v, want ready with instance URL", body.Publication)
	}

	stored, err := dyn.Resource(asclient.ProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Project: %v", err)
	}
	envs, _, _ := unstructured.NestedSlice(stored.Object, "spec", "environments")
	env, _ := envs[0].(map[string]any)
	bindings, _ := env["bindings"].([]any)
	binding, _ := bindings[0].(map[string]any)
	values, _ := binding["values"].(map[string]any)
	if values["access"] != "public" {
		t.Fatalf("binding values access = %#v, want public", values["access"])
	}
	if values["name"] != "demo-prod" {
		t.Fatalf("binding values were clobbered: %#v", values)
	}
}

func TestPublishReportsPendingUntilInstanceConverges(t *testing.T) {
	// The live instance still runs private while the binding asks for public.
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
	)
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing", `{"mode":"public"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", rec.Code, rec.Body.String())
	}
	var body projectPublishingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Publication == nil || body.Publication.Ready || body.Publication.Phase != "Pending" {
		t.Fatalf("publication = %+v, want pending until spec.access converges", body.Publication)
	}
}

func TestUnpublishedProjectReportsNotPublished(t *testing.T) {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
	}
	object, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	dyn := publishingTestDynamic(&unstructured.Unstructured{Object: object})
	router := publishingTestServer(t, dyn)

	rec := publishingDo(t, router, http.MethodGet, "/api/projects/demo/publishing", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", rec.Code, rec.Body.String())
	}
	var body projectPublishingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Published || body.Publication != nil {
		t.Fatalf("response = %+v, want not published", body)
	}
	// Publishing without a promoted production instance is a validation error.
	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing", `{"mode":"public"}`)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish without production status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGrantLifecycleWritesRBACOnly(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
	)
	router := publishingTestServer(t, dyn, publishingMember{User: "bob", RBACIdentity: "kedge:bob@example.com", Role: "member"})

	// Email identities are rejected.
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"bob@example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("email grant status = %d: %s", rec.Code, rec.Body.String())
	}
	// Non-members are rejected.
	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"mallory"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-member grant status = %d: %s", rec.Code, rec.Body.String())
	}

	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"bob"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant status = %d: %s", rec.Code, rec.Body.String())
	}
	var grants ListResponse[projectPublishingGrantView]
	if err := json.Unmarshal(rec.Body.Bytes(), &grants); err != nil {
		t.Fatalf("decode grants: %v", err)
	}
	if len(grants.Items) != 1 || grants.Items[0].User != "bob" || grants.Items[0].Phase != "Active" {
		t.Fatalf("grants = %+v, want one active grant for bob", grants.Items)
	}

	// The grant is exactly one ClusterRole + one ClusterRoleBinding carrying
	// the access-subresource tuple the hub SAR checks.
	role, err := dyn.Resource(clusterRoleGVR).Get(context.Background(), appAccessRoleName("demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	if len(rules) != 2 {
		t.Fatalf("ClusterRole rules = %#v, want access tuple + workspace access", rules)
	}
	rule, _ := rules[0].(map[string]any)
	resources, _ := rule["resources"].([]any)
	names, _ := rule["resourceNames"].([]any)
	if len(resources) != 1 || resources[0] != "applications/access" || len(names) != 1 || names[0] != "demo-prod" {
		t.Fatalf("ClusterRole rule = %#v, want applications/access on demo-prod", rule)
	}
	// kcp's workspace content authorizer requires `access` on "/" before any
	// RBAC rule applies — without this, invited outsiders are denied even
	// with a perfect grant.
	wsRule, _ := rules[1].(map[string]any)
	wsURLs, _ := wsRule["nonResourceURLs"].([]any)
	wsVerbs, _ := wsRule["verbs"].([]any)
	if len(wsURLs) != 1 || wsURLs[0] != "/" || len(wsVerbs) != 1 || wsVerbs[0] != "access" {
		t.Fatalf("ClusterRole workspace rule = %#v, want access on /", wsRule)
	}
	binding, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("demo-prod", "bob"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRoleBinding: %v", err)
	}
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	subject, _ := subjects[0].(map[string]any)
	// The subject must be the kcp RBAC identity — the username kcp actually
	// evaluates — never the User CR name (no kcp binding references it).
	if subject["kind"] != "User" || subject["name"] != "kedge:bob@example.com" {
		t.Fatalf("binding subject = %#v, want User kedge:bob@example.com", subject)
	}

	// Idempotent re-grant.
	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"bob"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-grant status = %d: %s", rec.Code, rec.Body.String())
	}

	// Revoke deletes the binding.
	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants/"+appAccessBindingName("demo-prod", "bob"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("demo-prod", "bob"), metav1.GetOptions{}); !strings.Contains(err.Error(), "not found") {
		t.Fatalf("binding survived revoke: %v", err)
	}
}

func TestGrantInviteByEmailProvisionsThroughHubAndWritesRBAC(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
	)
	client := asclient.NewFromDynamic(dyn)
	var invitedEmail string
	server := &Server{
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		publishingMembershipFetcher: func(context.Context, identity) ([]publishingMember, error) {
			return nil, nil // the invitee is not a member yet
		},
		publishingMemberInviter: func(_ context.Context, _ identity, email string) (publishingMember, error) {
			invitedEmail = email
			return publishingMember{User: "user-carol", RBACIdentity: "kedge:carol@example.com"}, nil
		},
	}
	router := mux.NewRouter()
	server.Register(router)

	// Without invite, an email is still rejected.
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("plain email grant status = %d: %s", rec.Code, rec.Body.String())
	}

	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite status = %d: %s", rec.Code, rec.Body.String())
	}
	if invitedEmail != "carol@example.com" {
		t.Fatalf("hub invite got %q", invitedEmail)
	}
	var grants ListResponse[projectPublishingGrantView]
	if err := json.Unmarshal(rec.Body.Bytes(), &grants); err != nil {
		t.Fatalf("decode grants: %v", err)
	}
	// The grant is written against the pending User's stable name, never the
	// email, so it is live the moment the invitee first signs in.
	if len(grants.Items) != 1 || grants.Items[0].User != "user-carol" {
		t.Fatalf("grants = %+v, want one active grant for user-carol", grants.Items)
	}
	if _, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("demo-prod", "user-carol"), metav1.GetOptions{}); err != nil {
		t.Fatalf("invited grant binding missing: %v", err)
	}
}

func TestGrantCreationRequiresPrivateAccess(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "public"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "public", "https://demo-prod-abc.apps.test"),
	)
	router := publishingTestServer(t, dyn, publishingMember{User: "bob", RBACIdentity: "kedge:bob@example.com"})
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"bob"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "private access") {
		t.Fatalf("public-mode grant status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRevokeIsAllowedInAnyMode(t *testing.T) {
	// A stale grant on a now-public app must remain revocable.
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "public"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "public", "https://demo-prod-abc.apps.test"),
		staleGrantBinding("demo-prod", "bob"),
	)
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants/"+appAccessBindingName("demo-prod", "bob"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRevokeRejectsForeignBinding(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
		staleGrantBinding("other-app", "bob"),
	)
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants/"+appAccessBindingName("other-app", "bob"), "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("foreign revoke status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("other-app", "bob"), metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign binding was deleted: %v", err)
	}
}

func TestUnpublishGoesPrivateAndRemovesGrants(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "public"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "public", "https://demo-prod-abc.apps.test"),
		staleGrantBinding("demo-prod", "bob"),
	)
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodDelete, "/api/projects/demo/publishing", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unpublish status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := dyn.Resource(asclient.ProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Project: %v", err)
	}
	envs, _, _ := unstructured.NestedSlice(stored.Object, "spec", "environments")
	env, _ := envs[0].(map[string]any)
	bindings, _ := env["bindings"].([]any)
	binding, _ := bindings[0].(map[string]any)
	values, _ := binding["values"].(map[string]any)
	if values["access"] != "private" {
		t.Fatalf("binding access after unpublish = %#v, want private", values["access"])
	}
	if _, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("demo-prod", "bob"), metav1.GetOptions{}); err == nil {
		t.Fatal("grants survived unpublish")
	}
	// The production instance itself is untouched.
	if _, err := dyn.Resource(publishingTestTargetGVR).Get(context.Background(), "demo-prod", metav1.GetOptions{}); err != nil {
		t.Fatalf("production instance was touched by unpublish: %v", err)
	}
}

func staleGrantBinding(instance, user string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata": map[string]any{
			"name": appAccessBindingName(instance, user),
			"labels": map[string]any{
				appAccessLabel:     instance,
				appAccessUserLabel: user,
			},
		},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": user}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessRoleName(instance)},
	}}
}
