// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

var actionSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,252}$`)
var actionVersion = regexp.MustCompile(`^v[1-9][0-9]{0,7}$`)

func providerActionCluster(req *http.Request) string {
	if req == nil || (req.Method != http.MethodGet && req.Method != http.MethodPost) || req.URL.RawPath != "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")
	if len(parts) != 10 || parts[0] != "services" || parts[1] != "providers" || parts[3] != "actions" || parts[4] != "clusters" || !actionVersion.MatchString(parts[9]) {
		return ""
	}
	for _, i := range []int{2, 5, 6, 7, 8} {
		if !actionSegment.MatchString(parts[i]) || parts[i] == "." || parts[i] == ".." {
			return ""
		}
	}
	return parts[5]
}

func (r *kcpTenantResolver) resolveTenantActionServiceAccount(req *http.Request) (string, string, error) {
	cluster := providerActionCluster(req)
	if cluster == "" || r.workloadConfig == nil {
		return "", "", errors.New("ordinary ServiceAccount requires a Provider Action route")
	}
	org, workspace := req.Header.Get(headerRailgridOrg), req.Header.Get(headerRailgridWorkspace)
	if !actionSegment.MatchString(org) || !actionSegment.MatchString(workspace) {
		return "", "", errors.New("concrete tenant selection required")
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", "", errors.New("bearer identity required")
	}
	cfg := r.workloadConfig.ChildWorkspaceConfig(org, workspace)
	user, err := serviceaccounts.VerifyProviderActionServiceAccount(req.Context(), cfg, strings.TrimPrefix(auth, "Bearer "))
	if err != nil {
		return "", "", err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", "", err
	}
	logical, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(req.Context(), "cluster", metav1.GetOptions{})
	if err != nil || logical.GetAnnotations()["kcp.io/cluster"] != cluster {
		return "", "", errors.New("action route does not match authenticated tenant")
	}
	return user, workspacePathRoot + ":" + org + ":" + workspace, nil
}
