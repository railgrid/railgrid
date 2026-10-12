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
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/railgrid/provider-app-studio/bindings"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorilla/mux"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestAuthenticationGrantPublicAppDoesNotCreateMembership(t *testing.T) {
	p := publishingTestProjectTyped("demo", "owned-project", "public")
	p.Spec.Authentication.Enabled = true
	dyn := publishingTestDynamic(publishingTestProjectObject(t, p), publishingTestTarget("demo-prod", "instance-uid", "public", "https://demo.example"))
	c := asclient.NewFromDynamic(dyn)
	lookups := 0
	var brokerRequests []appAccessBrokerRequest
	s := &Server{tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "ws-1")}.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
		projectClientFor: func(identity) (*asclient.Client, error) { return c, nil },
		appAccessBroker: func(_ context.Context, _ identity, req appAccessBrokerRequest) ([]projectPublishingGrantView, error) {
			brokerRequests = append(brokerRequests, req)
			if req.Operation != appAccessBrokerCreate || req.User != "outside-user" || req.ProjectUID != string(p.UID) {
				t.Fatalf("unexpected authentication grant broker request: %+v", req)
			}
			return []projectPublishingGrantView{{Name: "grant-demo-outside-user", User: req.User, Publication: req.Name, Phase: "Active"}}, nil
		},
		integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
			return attrs.Resource == "projects" && attrs.Name == "demo" && attrs.Verb == "update", nil
		},
		authenticationUserResolver: func(_ context.Context, _ identity, target projectPublishingTargetView, user string) (publishingMember, error) {
			lookups++
			if target.Name != "demo-prod" || user != "outside-user" {
				t.Fatal("lookup scope changed")
			}
			return publishingMember{User: user, RBACIdentity: "railgrid:outside"}, nil
		},
		publishingMemberInviter: func(context.Context, identity, string) (publishingMember, error) {
			t.Fatal("auth grant tried to invite a member")
			return publishingMember{}, nil
		},
	}
	r := mux.NewRouter()
	r.HandleFunc("/api/projects/{project}/authentication-grants", s.createProjectAuthenticationGrant).Methods(http.MethodPost)
	result := publishingDo(t, r, http.MethodPost, "/api/projects/demo/authentication-grants", `{"user":"outside-user","appId":"owned-project/production"}`)
	if result.Code != http.StatusOK {
		t.Fatalf("grant failed: %d %s", result.Code, result.Body.String())
	}
	if lookups != 1 {
		t.Fatalf("lookups=%d", lookups)
	}
	if len(brokerRequests) != 1 || brokerRequests[0].Name != "demo-prod" || brokerRequests[0].UID != "instance-uid" {
		t.Fatalf("authentication grant broker requests=%+v, want one request for the exact managed Instance", brokerRequests)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" && (a.GetResource().Resource == "memberships" || a.GetResource().Resource == "users") {
			t.Fatal("grant created platform membership/account")
		}
	}
	result = publishingDo(t, r, http.MethodPost, "/api/projects/demo/authentication-grants", `{"user":"outside-user","appId":"other-project/production"}`)
	if result.Code != http.StatusBadRequest || lookups != 1 || len(brokerRequests) != 1 {
		t.Fatalf("cross-project grant not rejected: %d lookups=%d broker calls=%d", result.Code, lookups, len(brokerRequests))
	}
}

func TestAuthenticationToolsDoNotRequireMCPIdentity(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	for _, name := range []string{"get_project_authentication", "configure_project_authentication"} {
		spec, ok := registry.Spec(name)
		if !ok || spec.RequiresProjectMCPIdentity {
			t.Fatalf("invalid auth tool %s", name)
		}
	}
}

func TestGetProjectAuthenticationToolAuthorizesCallerBeforeProviderReads(t *testing.T) {
	project := publishingTestProjectTyped("demo", "project-uid", "private")
	caller := identity{clusterID: "cluster-a", user: "alice"}
	toolFor := func(server *Server) projectAssistantTool {
		t.Helper()
		for _, tool := range projectAuthenticationTools(server) {
			if tool.Spec().Name == "get_project_authentication" {
				return tool
			}
		}
		t.Fatal("get_project_authentication tool was not registered")
		return nil
	}

	t.Run("denied caller causes no provider reads", func(t *testing.T) {
		dyn := publishingTestDynamic(publishingTestProjectObject(t, project))
		client := asclient.NewFromDynamic(dyn)
		clientFactoryCalls := 0
		reviewCalls := 0
		var reviewed dataplane.ResourceAttributes
		server := &Server{
			projectClientFor: func(identity) (*asclient.Client, error) {
				clientFactoryCalls++
				return client, nil
			},
			integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
				reviewCalls++
				reviewed = attrs
				return false, nil
			},
		}

		result, err := toolFor(server).Call(context.Background(), projectAssistantToolCallRequest{
			Identity: caller,
			Project:  project.DeepCopy(),
		})
		if result != "" || !apierrors.IsForbidden(err) {
			t.Fatalf("tool result/error = %q / %v, want empty result and forbidden", result, err)
		}
		if clientFactoryCalls != 0 || len(dyn.Actions()) != 0 {
			t.Fatalf("denied read created %d clients and made provider actions %v, want none", clientFactoryCalls, dyn.Actions())
		}
		want := dataplane.ResourceAttributes{
			Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource,
			Name: project.Name, Verb: "get",
		}
		if reviewCalls != 1 || reviewed != want {
			t.Fatalf("authorization reviews=%d attrs=%+v, want one exact Project get review %+v", reviewCalls, reviewed, want)
		}
		if len(err.Error()) > 200 || !strings.Contains(err.Error(), "project get permission required") {
			t.Fatalf("forbidden error = %v, want a bounded Project get denial", err)
		}
	})

	t.Run("allowed caller still checks provider Project UID before related reads", func(t *testing.T) {
		stored := publishingTestProjectTyped("demo", "stored-project-uid", "private")
		dyn := publishingTestDynamic(
			publishingTestProjectObject(t, stored),
			applicationTemplateObject(),
			publishingTestTarget("demo-prod", "instance-uid", "private", "https://demo.example"),
		)
		client := asclient.NewFromDynamic(dyn)
		clientFactoryCalls := 0
		reviewCalls := 0
		var reviewed dataplane.ResourceAttributes
		server := &Server{
			projectClientFor: func(identity) (*asclient.Client, error) {
				clientFactoryCalls++
				return client, nil
			},
			integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
				reviewCalls++
				reviewed = attrs
				return true, nil
			},
		}

		_, err := toolFor(server).Call(context.Background(), projectAssistantToolCallRequest{
			Identity: caller,
			Project:  project.DeepCopy(),
		})
		if err == nil || !strings.Contains(err.Error(), "project identity changed") {
			t.Fatalf("tool error = %v, want Project UID mismatch", err)
		}
		want := dataplane.ResourceAttributes{
			Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource,
			Name: project.Name, Verb: "get",
		}
		if reviewCalls != 1 || reviewed != want {
			t.Fatalf("authorization reviews=%d attrs=%+v, want one exact Project get review %+v", reviewCalls, reviewed, want)
		}
		if clientFactoryCalls != 1 {
			t.Fatalf("provider client factory calls=%d, want one after authorization", clientFactoryCalls)
		}
		actions := dyn.Actions()
		if len(actions) != 1 || actions[0].GetResource().Resource != projectsGVR.Resource || actions[0].GetVerb() != "get" {
			t.Fatalf("provider actions before UID rejection = %v, want only the named Project get", actions)
		}
	})
}

func TestAuthenticationConfigurationPreservesSharingAndRequiresCompatibility(t *testing.T) {
	p := publishingTestProjectTyped("demo", "owned-project", "private")
	template := applicationTemplateObject()
	properties := map[string]any{"railgridAuth": map[string]any{"type": "boolean"}, "railgridAuthAppID": map[string]any{"type": "string"}, "railgridAuthEnvironment": map[string]any{"type": "string"}, "railgridAuthInstanceUID": map[string]any{"type": "string"}}
	if err := unstructured.SetNestedMap(template.Object, properties, "spec", "schema", "properties"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(template.Object, map[string]any{"api": map[string]any{"workspacePath": "api", "devImage": "node", "startCommand": "npm start", "port": "api", "imageInput": "apiImage"}}, "spec", "development", "components"); err != nil {
		t.Fatal(err)
	}
	dyn := publishingTestDynamic(publishingTestProjectObject(t, p), template)
	c := asclient.NewFromDynamic(dyn)
	s := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return true, nil }, authenticationServiceProbe: func(context.Context) error { return errors.New("old hub") }}
	_, err := s.configureAuthentication(context.Background(), c, identity{}, p, true)
	if err == nil {
		t.Fatal("incompatible hub enabled authentication")
	}
	saved, err := c.Projects().Get(context.Background(), p.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Spec.Authentication.Enabled {
		t.Fatal("compatibility failure changed Project")
	}
	s.authenticationServiceProbe = func(context.Context) error { return nil }
	saved, err = s.configureAuthentication(context.Background(), c, identity{}, p, true)
	if err != nil {
		t.Fatal(err)
	}
	values, err := bindings.Values(saved.Spec.Environments[0].Bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	if values["access"] != "private" || values[bindings.AuthAppIDField] != "owned-project/production" || !saved.Spec.Authentication.Enabled {
		t.Fatalf("auth changed sharing or lost identity: %#v", saved.Spec)
	}
	s.authenticationServiceProbe = func(context.Context) error { t.Fatal("idempotent configure queried unavailable hub"); return nil }
	if _, err = s.configureAuthentication(context.Background(), c, identity{}, saved, true); err != nil {
		t.Fatal(err)
	}
	s.integrationAccessReviewer = func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return false, nil }
	if _, err = s.configureAuthentication(context.Background(), c, identity{}, saved, false); !apierrors.IsForbidden(err) {
		t.Fatalf("unauthorized disable error=%v", err)
	}
}
