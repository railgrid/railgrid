/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"net/http/httptest"
	"testing"
)

func TestOrgProviderRoutePinsCanonicalCluster(t *testing.T) {
	for _, path := range []string{
		"clusters/other/api/v1/configmaps", "clusters/root:tenants:org-a/api/v1/configmaps",
		"clusters/cluster-a/api/v1/../secrets", "clusters/cluster-a/api/v1/namespaces//secrets",
		"clusters/cluster-a/api/v1/%73ecrets", "clusters/cluster-a/api/v1/configmaps/one%2Ftwo",
	} {
		r := httptest.NewRequest("GET", "/"+path, nil)
		if _, err := parseOrgProviderRoute(r, path, "cluster-a"); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if canonicalOrgProviderClusterID("root:tenants:org-a") {
		t.Fatal("workspace path accepted as a logical cluster ID")
	}
}

func TestOrgProviderRouteDistinguishesNamespaceObjectsFromNamespacedResources(t *testing.T) {
	for _, tc := range []struct{ path, resource, name, namespace, subresource string }{
		{"api/v1/namespaces", "namespaces", "", "", ""},
		{"api/v1/namespaces/default", "namespaces", "default", "", ""},
		{"api/v1/namespaces/default/status", "namespaces", "default", "", "status"},
		{"api/v1/namespaces/default/finalize", "namespaces", "default", "", "finalize"},
		{"api/v1/namespaces/default/secrets/one", "secrets", "one", "default", ""},
	} {
		path := "clusters/cluster-a/" + tc.path
		route, err := parseOrgProviderRoute(httptest.NewRequest("GET", "/"+path, nil), path, "cluster-a")
		if err != nil || route.resource != tc.resource || route.name != tc.name || route.namespace != tc.namespace || route.subresource != tc.subresource {
			t.Errorf("%s = %+v, %v", tc.path, route, err)
		}
	}
}
