// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var selfSubjectReviews = schema.GroupVersionResource{
	Group: "authentication.k8s.io", Version: "v1", Resource: "selfsubjectreviews",
}

// resolveAgentAttachmentOwner asks the caller-authenticated API server for
// the Kubernetes principal attached to this bearer. The hub's browser user ID
// is a Railgrid User UUID and is not necessarily the username KCP stores on a
// data-plane request, so it cannot own upload bytes directly.
func (s *Server) resolveAgentAttachmentOwner(ctx context.Context, id identity) (string, error) {
	if s == nil || s.tenant == nil || strings.TrimSpace(id.clusterID) == "" || strings.TrimSpace(id.token) == "" {
		return "", errors.New("a current caller bearer is required to resolve Agent attachment ownership")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	scope, err := s.tenant.For(id.clusterID, id.token)
	if err != nil {
		return "", fmt.Errorf("build caller identity lookup: %w", err)
	}
	request := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "SelfSubjectReview",
		"spec":       map[string]any{},
	}}
	response, err := scope.Dynamic().Resource(selfSubjectReviews).Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("resolve current caller identity: %w", err)
	}
	username, found, err := unstructured.NestedString(response.Object, "status", "userInfo", "username")
	username = strings.TrimSpace(username)
	if err != nil || !found || username == "" || username == "system:anonymous" {
		return "", errors.New("the current caller identity response is incomplete")
	}
	return username, nil
}
