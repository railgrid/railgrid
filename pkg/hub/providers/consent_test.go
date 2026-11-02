// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

func TestConsentDigestTracksIdentityAndPermissionsOnly(t *testing.T) {
	base := Provider{Name: "planner", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:planner", APIExportName: "planner.example"}
	original := ConsentDigest(base)
	if len(original) != 64 || original != ConsentDigest(base) {
		t.Fatal("missing or unstable digest")
	}
	for _, field := range []string{"owner", "name", "export path", "export name", "requires", "hub access"} {
		t.Run(field, func(t *testing.T) {
			p := base
			switch field {
			case "owner":
				p.OrgUUID = "org-b"
			case "name":
				p.Name = "other"
			case "export path":
				p.APIExportPath += ":other"
			case "export name":
				p.APIExportName = "other.example"
			case "requires":
				p.Requires = []providersv1alpha1.ProviderRequirement{{Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{"delete"}}}}}
			case "hub access":
				p.HubAccess = []providersv1alpha1.ProviderHubAccess{{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}}
			}
			if ConsentDigest(p) == original {
				t.Fatal("changed permission identity kept old consent digest")
			}
		})
	}
	base.DisplayName, base.Description, base.Version = "New title", "New description", "2"
	base.BackendHealthy = true
	if ConsentDigest(base) != original {
		t.Fatal("display or health changes invalidated permission consent")
	}
}

func TestListProviderPublishesConsentDigest(t *testing.T) {
	p := Provider{Name: "planner", APIExportPath: "root:railgrid:providers:planner", APIExportName: "planner.example"}
	registry := NewRegistry()
	registry.Upsert(p)
	response := httptest.NewRecorder()
	NewListHandler(registry).ServeHTTP(response, httptest.NewRequest(http.MethodGet, PathListProviders, nil))
	var listing struct {
		Items []struct {
			ConsentDigest string `json:"consentDigest"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 || listing.Items[0].ConsentDigest != ConsentDigest(p) {
		t.Fatalf("missing declaration digest: %s", response.Body.String())
	}
}
