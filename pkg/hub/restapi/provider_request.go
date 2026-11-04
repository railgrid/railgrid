/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

const orgProviderAuthorizationTimeout = 2 * time.Second

var errOrgProviderAuthorizationDenied = errors.New("organization provider authorization denied")

type orgProviderRoute struct {
	group       string
	version     string
	resource    string
	name        string
	subresource string
	namespace   string
}

func parseOrgProviderRoute(r *http.Request, rest, expectedCluster string) (orgProviderRoute, error) {
	if rest == "" || strings.Contains(rest, "%") || strings.ContainsAny(rest, "\\") || strings.Contains(rest, "//") || r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
		return orgProviderRoute{}, fmt.Errorf("organization provider path is not canonical")
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 5 || parts[0] != "clusters" || parts[1] != expectedCluster || !canonicalOrgProviderClusterID(expectedCluster) {
		return orgProviderRoute{}, fmt.Errorf("organization provider must target the selected workspace cluster")
	}
	group, version, pathStart := "", "", 0
	switch parts[2] {
	case "api":
		version = parts[3]
		pathStart = 4
	case "apis":
		if len(parts) < 6 || parts[3] == "" {
			return orgProviderRoute{}, fmt.Errorf("invalid Kubernetes API path")
		}
		group, version, pathStart = parts[3], parts[4], 5
	default:
		return orgProviderRoute{}, fmt.Errorf("only Kubernetes API paths are allowed")
	}
	if version == "" || pathStart >= len(parts) {
		return orgProviderRoute{}, fmt.Errorf("organization provider requires a Kubernetes API resource path")
	}
	for _, segment := range parts {
		if segment == "" || segment == "." || segment == ".." {
			return orgProviderRoute{}, fmt.Errorf("organization provider path is not canonical")
		}
	}
	apiPath := parts[pathStart:]
	resourceIndex, namespace := 0, ""
	if apiPath[0] == "namespaces" && len(apiPath) > 2 && (group != "" || apiPath[2] != "status" && apiPath[2] != "finalize") {
		if len(apiPath) < 3 || apiPath[1] == "" {
			return orgProviderRoute{}, fmt.Errorf("namespace resource path is incomplete")
		}
		namespace, resourceIndex = apiPath[1], 2
	}
	if resourceIndex >= len(apiPath) {
		return orgProviderRoute{}, fmt.Errorf("organization provider requires a Kubernetes API resource path")
	}
	name, subresource := "", ""
	if len(apiPath) > resourceIndex+1 {
		name = apiPath[resourceIndex+1]
	}
	if len(apiPath) > resourceIndex+2 {
		subresource = apiPath[resourceIndex+2]
	}
	return orgProviderRoute{group: group, version: version, resource: apiPath[resourceIndex], name: name, subresource: subresource, namespace: namespace}, nil
}

func canonicalOrgProviderClusterID(value string) bool {
	return value != "" && len(validation.IsDNS1123Label(value)) == 0
}

func orgProviderCoordinateDeclared(prov providers.Provider, group, version, resource, verb string) bool {
	if prov.Export == nil {
		return false
	}
	for _, declared := range prov.Export.Coordinates() {
		gv := declared.GroupVersion()
		if declared.APIVersion == group+"/"+version && gv.Group == group && declared.Resource == resource && declared.Verb == verb {
			return true
		}
	}
	return false
}

func orgProviderBoundResource(resources []kcp.BoundResource, group, resource string) bool {
	for _, bound := range resources {
		if bound.Group == group && bound.Resource == resource {
			return true
		}
	}
	return false
}

func containsOrgProviderGroup(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
