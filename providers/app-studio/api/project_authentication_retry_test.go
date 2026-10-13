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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-app-studio/bindings"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestConfigureAuthenticationRepairsBindingsWithoutChangingFlag(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			p := publishingTestProjectTyped("demo", "owned-project", "private")
			p.Spec.Authentication.Enabled = enabled
			stale, err := json.Marshal(map[string]any{
				"access": "private", bindings.AuthEnabledField: !enabled,
				bindings.AuthAppIDField: "old-project/production", bindings.AuthEnvironmentField: "production",
				"railgridAuthInstanceUID": "untrusted-instance-uid",
			})
			if err != nil {
				t.Fatal(err)
			}
			p.Spec.Environments[0].Bindings[0].Values.Raw = stale
			added := p.Spec.Environments[0].DeepCopy()
			added.Name = "development"
			p.Spec.Environments = append(p.Spec.Environments, *added)
			template := applicationTemplateObject()
			properties := map[string]any{
				bindings.AuthEnabledField:     map[string]any{"type": "boolean"},
				bindings.AuthAppIDField:       map[string]any{"type": "string"},
				bindings.AuthEnvironmentField: map[string]any{"type": "string"},
				"railgridAuthInstanceUID":     map[string]any{"type": "string"},
			}
			if err := unstructured.SetNestedMap(template.Object, properties, "spec", "schema", "properties"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedMap(template.Object, map[string]any{"api": map[string]any{"workspacePath": "api", "port": "api"}}, "spec", "development", "components"); err != nil {
				t.Fatal(err)
			}
			dyn := publishingTestDynamic(publishingTestProjectObject(t, p), template)
			client := asclient.NewFromDynamic(dyn)
			server := &Server{
				integrationAccessReviewer:  func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return true, nil },
				authenticationServiceProbe: func(context.Context) error { return nil },
			}
			saved, err := server.configureAuthentication(context.Background(), client, identity{}, p, enabled)
			if err != nil {
				t.Fatal(err)
			}
			for _, env := range saved.Spec.Environments {
				values, err := bindings.Values(env.Bindings[0])
				if err != nil {
					t.Fatal(err)
				}
				if values["access"] != "private" || values[bindings.AuthEnabledField] != enabled || values[bindings.AuthAppIDField] != "owned-project/"+env.Name || values[bindings.AuthEnvironmentField] != env.Name {
					t.Fatalf("binding did not converge for %s: %#v", env.Name, values)
				}
				if _, present := values["railgridAuthInstanceUID"]; present {
					t.Fatal("binding retained caller-supplied Instance UID")
				}
			}
			updates := 0
			for _, action := range dyn.Actions() {
				if action.GetVerb() == "update" {
					updates++
				}
			}
			if updates != 1 {
				t.Fatalf("updates=%d, want one repair", updates)
			}
			dyn.ClearActions()
			server.authenticationServiceProbe = func(context.Context) error { t.Fatal("converged retry probed hub"); return nil }
			if _, err := server.configureAuthentication(context.Background(), client, identity{}, saved, enabled); err != nil {
				t.Fatal(err)
			}
			for _, action := range dyn.Actions() {
				if action.GetVerb() == "update" {
					t.Fatal("converged retry updated Project")
				}
			}
		})
	}
}

func TestConfigureAuthenticationRejectsReplacedProjectWithConflict(t *testing.T) {
	p := publishingTestProjectTyped("demo", "old-project", "private")
	replacement := p.DeepCopy()
	replacement.UID = "new-project"
	dyn := publishingTestDynamic(publishingTestProjectObject(t, replacement))
	server := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return true, nil }}
	_, err := server.configureAuthentication(context.Background(), asclient.NewFromDynamic(dyn), identity{}, p, true)
	if !apierrors.IsConflict(err) {
		t.Fatalf("error=%v, want Conflict", err)
	}
	response := httptest.NewRecorder()
	writeError(response, err)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", response.Code)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("replaced Project caused mutation: %v", action)
		}
	}
}
