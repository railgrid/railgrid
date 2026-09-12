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
	"io"
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
	publishingTestTargetGVR = schema.GroupVersionResource{Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "instances"}
	clusterRoleGVR          = clusterRoleResource.GVR
	clusterRoleBindingGVR   = clusterRoleBindingResource.GVR
)

func publishingTestDynamic(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR:     "ProjectList",
			publishingTestTargetGVR: "InstanceList",
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
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "ws-1")}.lookup,
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
						Name: name + "-prod", APIVersion: publishingTestTargetGVR.GroupVersion().String(), Kind: "Instance", Resource: "instances",
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
		"apiVersion": publishingTestTargetGVR.GroupVersion().String(), "kind": "Instance",
		"metadata": map[string]any{"name": name, "uid": uid},
		"spec":     map[string]any{"template": "application", "values": map[string]any{"access": specAccess}},
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
	r.Header.Set("X-Faros-Tenant", "cluster-a")
	r.Header.Set("X-Faros-Cluster", "cluster-a")
	r.Header.Set("X-Faros-User", "alice")
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

func TestRequestedPublishingModeVocabulary(t *testing.T) {
	project := publishingTestProjectTyped("demo", "uid", "")
	for input, want := range map[string]struct {
		access string
		policy aiv1alpha1.ProjectSharingMode
	}{
		"public":     {accessPublic, aiv1alpha1.ProjectSharingModePublic},
		"restricted": {accessPrivate, aiv1alpha1.ProjectSharingModeShared},
		"members":    {accessPrivate, aiv1alpha1.ProjectSharingModeShared},
		"private":    {accessPrivate, aiv1alpha1.ProjectSharingModeShared},
	} {
		access, policy, err := requestedPublishingMode(input, project)
		if err != nil || access != want.access || policy != want.policy {
			t.Fatalf("requestedPublishingMode(%q) = %q, %q, %v; want %q, %q", input, access, policy, err, want.access, want.policy)
		}
	}
	if _, _, err := requestedPublishingMode("bogus", project); err == nil {
		t.Fatal("requestedPublishingMode accepted an unknown mode")
	}
	// Empty preserves an explicit public setting, otherwise defaults to
	// invite-only.
	publicProject := publishingTestProjectTyped("demo", "uid", accessPublic)
	if access, policy, _ := requestedPublishingMode("", publicProject); access != accessPublic || policy != aiv1alpha1.ProjectSharingModePublic {
		t.Fatalf("empty mode on public project = %q/%q, want public", access, policy)
	}
	if access, policy, _ := requestedPublishingMode("", project); access != accessPrivate || policy != aiv1alpha1.ProjectSharingModeShared {
		t.Fatalf("empty mode on unconfigured project = %q/%q, want private/shared", access, policy)
	}
}

// Invite-only and unpublished both run with private access; what tells them
// apart is the recorded policy or, for a project that predates it, whether
// anyone actually holds a grant. A fresh promote is unpublished.
func TestPublishingStateDistinguishesRestrictedFromUnpublished(t *testing.T) {
	grant := []projectPublishingGrantView{{Name: "g", User: "bob"}}
	revoked := []projectPublishingGrantView{{Name: "g", User: "bob", Revoked: true}}
	withPolicy := func(mode aiv1alpha1.ProjectSharingMode) *aiv1alpha1.Project {
		p := publishingTestProjectTyped("demo", "uid", "private")
		p.Spec.Sharing.Publishing.Mode = mode
		return p
	}
	for _, test := range []struct {
		name      string
		project   *aiv1alpha1.Project
		access    string
		grants    []projectPublishingGrantView
		published bool
		mode      string
	}{
		{name: "public access", project: withPolicy(""), access: accessPublic, published: true, mode: "public"},
		{name: "shared policy without grants", project: withPolicy(aiv1alpha1.ProjectSharingModeShared), access: accessPrivate, published: true, mode: "restricted"},
		{name: "private policy without grants", project: withPolicy(aiv1alpha1.ProjectSharingModePrivate), access: accessPrivate, published: false, mode: "private"},
		{name: "private policy with a grant", project: withPolicy(aiv1alpha1.ProjectSharingModePrivate), access: accessPrivate, grants: grant, published: true, mode: "restricted"},
		{name: "legacy project without grants", project: withPolicy(""), access: accessPrivate, published: false, mode: "private"},
		{name: "legacy project with a grant", project: withPolicy(""), access: accessPrivate, grants: grant, published: true, mode: "restricted"},
		{name: "revoked grants do not publish", project: withPolicy(""), access: accessPrivate, grants: revoked, published: false, mode: "private"},
	} {
		t.Run(test.name, func(t *testing.T) {
			published, mode := publishingState(test.project, appAccessRuntime{desiredAccess: test.access}, test.grants)
			if published != test.published || mode != test.mode {
				t.Fatalf("publishingState = %v, %q; want %v, %q", published, mode, test.published, test.mode)
			}
		})
	}
}

func TestPublishRestrictedRecordsSharedPolicy(t *testing.T) {
	dyn := publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
	)
	router := publishingTestServer(t, dyn)
	// Freshly promoted, never published: private.
	rec := publishingDo(t, router, http.MethodGet, "/api/projects/demo/publishing", "")
	var body projectPublishingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body.Published || body.Publication == nil || body.Publication.Mode != "private" {
		t.Fatalf("fresh promote = %d %+v, want unpublished with mode private", rec.Code, body)
	}
	rec = publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing", `{"mode":"restricted"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", rec.Code, rec.Body.String())
	}
	body = projectPublishingResponse{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Published || body.Publication == nil || body.Publication.Mode != "restricted" || !body.Publication.Ready {
		t.Fatalf("restricted publish = %+v, want published restricted and ready", body)
	}
	stored, err := dyn.Resource(asclient.ProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Project: %v", err)
	}
	if mode, _, _ := unstructured.NestedString(stored.Object, "spec", "sharing", "publishing", "mode"); mode != string(aiv1alpha1.ProjectSharingModeShared) {
		t.Fatalf("stored publishing mode = %q, want shared", mode)
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
	router := publishingTestServer(t, dyn, publishingMember{User: "bob", RBACIdentity: "faros:bob@example.com", Role: "member"})

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
	if len(resources) != 1 || resources[0] != "instances/access" || len(names) != 1 || names[0] != "demo-prod" {
		t.Fatalf("ClusterRole rule = %#v, want instances/access on demo-prod", rule)
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
	if subject["kind"] != "User" || subject["name"] != "faros:bob@example.com" {
		t.Fatalf("binding subject = %#v, want User faros:bob@example.com", subject)
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
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "ws-1")}.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		publishingMembershipFetcher: func(context.Context, identity) ([]publishingMember, error) {
			return nil, nil // the invitee is not a member yet
		},
		publishingMemberInviter: func(_ context.Context, _ identity, email string) (publishingMember, error) {
			invitedEmail = email
			return publishingMember{User: "user-carol", RBACIdentity: "faros:carol@example.com"}, nil
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
	router := publishingTestServer(t, dyn, publishingMember{User: "bob", RBACIdentity: "faros:bob@example.com"})
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
	var body projectPublishingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Published || body.Publication == nil || body.Publication.Mode != "private" || len(body.Grants) != 0 {
		t.Fatalf("unpublish response = %+v, want unpublished, mode private, no grants", body)
	}
	// A later read agrees: the app is unpublished, not "restricted".
	rec = publishingDo(t, router, http.MethodGet, "/api/projects/demo/publishing", "")
	body = projectPublishingResponse{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Published || body.Publication == nil || body.Publication.Mode != "private" {
		t.Fatalf("publishing after unpublish = %+v, want unpublished with mode private", body)
	}
	stored, err := dyn.Resource(asclient.ProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Project: %v", err)
	}
	if mode, _, _ := unstructured.NestedString(stored.Object, "spec", "sharing", "publishing", "mode"); mode != string(aiv1alpha1.ProjectSharingModePrivate) {
		t.Fatalf("stored publishing mode = %q, want private", mode)
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

// publishingHubStub stands in for the hub membership API: it records the
// invite it receives, answers it with a canned status and body, and serves the
// org and workspace rosters for org-a / ws-1 (the tenant setPublishingIdentity
// presents).
type publishingHubStub struct {
	URL          string
	inviteMethod string
	invitePath   string
	inviteHeader http.Header
	inviteBody   []byte
}

func newPublishingHubStub(t *testing.T, inviteStatus int, inviteBody string, orgRoster, wsRoster []publishingMember) *publishingHubStub {
	t.Helper()
	stub := &publishingHubStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/orgs/org-a/memberships":
			body, _ := io.ReadAll(r.Body)
			stub.inviteMethod, stub.invitePath, stub.inviteHeader, stub.inviteBody = r.Method, r.URL.Path, r.Header.Clone(), body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(inviteStatus)
			_, _ = w.Write([]byte(inviteBody))
		case r.Method == http.MethodGet && r.URL.Path == "/api/orgs/org-a/memberships":
			writeJSON(w, http.StatusOK, publishingMembersResponse{Items: orgRoster})
		case r.Method == http.MethodGet && r.URL.Path == "/api/orgs/org-a/workspaces/ws-1/memberships":
			writeJSON(w, http.StatusOK, publishingMembersResponse{Items: wsRoster})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	stub.URL = server.URL
	return stub
}

// publishingServerAgainstHub wires a Server that talks to the stub hub over
// HTTP — no fetcher/inviter fakes — so the request App Studio sends is what
// gets checked.
func publishingServerAgainstHub(t *testing.T, dyn *fake.FakeDynamicClient, hubURL string) *mux.Router {
	t.Helper()
	client := asclient.NewFromDynamic(dyn)
	server := &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "ws-1")}.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		hubBase:          hubURL,
	}
	router := mux.NewRouter()
	server.Register(router)
	return router
}

func publishingInviteDynamic() *fake.FakeDynamicClient {
	return publishingTestDynamic(
		publishingTestProject("demo", "project-uid", "private"),
		publishingTestTarget("demo-prod", "runtime-uid-1", "private", "https://demo-prod-abc.apps.test"),
	)
}

func publishingBindingSubject(t *testing.T, dyn *fake.FakeDynamicClient, instance, user string) string {
	t.Helper()
	binding, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName(instance, user), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("grant binding for %s missing: %v", user, err)
	}
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	if len(subjects) != 1 {
		t.Fatalf("binding subjects = %v, want one", subjects)
	}
	subject, _ := subjects[0].(map[string]any)
	name, _ := subject["name"].(string)
	return name
}

// The invite is one POST to the hub's org membership route, scoped to the
// caller's workspace as well as the org (a delegated token can only be
// verified there), with the hub's invite semantics: pre-provision a pending
// User for the email and make it an org member. The grant then binds the
// identity the hub reports.
func TestInviteByEmailPostsOrgMembershipScopedToWorkspace(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusCreated,
		`{"user":"user-carol","rbacIdentity":"faros:carol@example.com","email":"carol@example.com","role":"member","orgUUID":"org-a"}`,
		nil, nil)
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite status = %d: %s", rec.Code, rec.Body.String())
	}
	if hub.inviteMethod != http.MethodPost || hub.invitePath != "/api/orgs/org-a/memberships" {
		t.Fatalf("hub call = %s %s, want POST /api/orgs/org-a/memberships", hub.inviteMethod, hub.invitePath)
	}
	for header, want := range map[string]string{
		"Authorization":     "Bearer test-token",
		"X-Faros-Org":       "org-a",
		"X-Faros-Workspace": "ws-1",
		"X-Faros-User":      "alice",
		"Content-Type":      "application/json",
	} {
		if got := hub.inviteHeader.Get(header); got != want {
			t.Errorf("invite header %s = %q, want %q", header, got, want)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(hub.inviteBody, &body); err != nil {
		t.Fatalf("invite body %q: %v", hub.inviteBody, err)
	}
	if body["user"] != "carol@example.com" || body["role"] != "member" || body["invite"] != true || len(body) != 3 {
		t.Fatalf("invite body = %v, want user/role=member/invite=true", body)
	}
	if subject := publishingBindingSubject(t, dyn, "demo-prod", "user-carol"); subject != "faros:carol@example.com" {
		t.Fatalf("grant bound %q, want the hub-reported RBAC identity", subject)
	}
}

// A caller the hub will not let add members (not an admin there) gets the
// hub's own 403 and reason, exactly as through the portal — not a 500.
func TestInviteForbiddenByHubAnswersForbidden(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusForbidden,
		`{"kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"this endpoint requires admin role"}`,
		nil, nil)
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "this endpoint requires admin role") {
		t.Fatalf("status = %d: %s, want 403 with the hub's message", rec.Code, rec.Body.String())
	}
	list, err := dyn.Resource(clusterRoleBindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("a refused invite still wrote a grant: %v", list.Items)
	}
}

// When the hub reports the person already exists, the grant proceeds against
// the identity the roster carries for that email.
func TestInviteConflictProceedsWithExistingMember(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusConflict,
		`{"kind":"Status","status":"Failure","reason":"AlreadyExists","code":409,"message":"user carol@example.com already exists"}`,
		[]publishingMember{{User: "user-carol", RBACIdentity: "faros:carol@example.com", Email: "Carol@Example.com", Role: "member"}},
		nil)
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite status = %d: %s", rec.Code, rec.Body.String())
	}
	if subject := publishingBindingSubject(t, dyn, "demo-prod", "user-carol"); subject != "faros:carol@example.com" {
		t.Fatalf("grant bound %q, want the roster's RBAC identity", subject)
	}
}

// A conflict the roster cannot explain is reported as the hub's conflict.
func TestInviteConflictWithoutRosterMatchAnswersConflict(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusConflict,
		`{"kind":"Status","status":"Failure","reason":"AlreadyExists","code":409,"message":"user carol@example.com already exists"}`,
		[]publishingMember{{User: "bob", RBACIdentity: "faros:bob@example.com", Email: "bob@example.com"}}, nil)
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("status = %d: %s, want 409 with the hub's message", rec.Code, rec.Body.String())
	}
}

// The hub refusing App Studio's own credential is not the caller's fault and
// not an internal error: it is an upstream failure, surfaced with the hub's
// reason so it can be diagnosed.
func TestInviteRejectedCredentialAnswersBadGateway(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusUnauthorized,
		`{"kind":"Status","status":"Failure","reason":"Unauthorized","code":401,"message":"Unauthorized"}`, nil, nil)
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"carol@example.com","invite":true}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "HTTP 401") {
		t.Fatalf("status = %d: %s, want 502 naming the hub's answer", rec.Code, rec.Body.String())
	}
}

// Granting an existing member reads the rosters over HTTP and binds the
// rbacIdentity the hub reports; the merge across the org and workspace rows
// must keep it (it used to drop it, so every such grant failed with 502).
func TestGrantExistingMemberKeepsHubRBACIdentity(t *testing.T) {
	hub := newPublishingHubStub(t, http.StatusInternalServerError, "",
		[]publishingMember{{User: "bob", RBACIdentity: "faros:bob@example.com", Email: "bob@example.com", Role: "member"}},
		[]publishingMember{{User: "bob", Role: "admin"}})
	dyn := publishingInviteDynamic()
	router := publishingServerAgainstHub(t, dyn, hub.URL)

	rec := publishingDo(t, router, http.MethodPost, "/api/projects/demo/publishing/grants", `{"user":"bob"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant status = %d: %s", rec.Code, rec.Body.String())
	}
	if subject := publishingBindingSubject(t, dyn, "demo-prod", "bob"); subject != "faros:bob@example.com" {
		t.Fatalf("grant bound %q, want the org roster's RBAC identity", subject)
	}
	if hub.inviteMethod != "" {
		t.Fatal("granting an existing member must not invite")
	}
}
