// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Discovery is transient: never put the submitted key in a durable Operation,
// Connection, log, or response. Check caller RBAC before contacting Linear.
func newOnboardingHandler(caller func(*http.Request) (dynamic.Interface, error), teams func(context.Context, string, string) (linearapi.Page[linearapi.Team], error)) http.Handler {
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "discovery busy", http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		client, err := caller(r)
		if err != nil {
			http.Error(w, "identity required", http.StatusUnauthorized)
			return
		}
		review, err := client.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
			"spec": map[string]any{"resourceAttributes": map[string]any{"group": "linear.providers.faros.sh", "resource": "connections", "verb": "create"}},
		}}, metav1.CreateOptions{})
		if err != nil {
			http.Error(w, "workspace authorization unavailable", http.StatusForbidden)
			return
		}
		allowed, _, _ := unstructured.NestedBool(review.Object, "status", "allowed")
		if !allowed {
			http.Error(w, "connection creation permission required", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		var input struct {
			APIKey string `json:"apiKey"`
			After  string `json:"after"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || strings.TrimSpace(input.APIKey) == "" || len(input.APIKey) > 4096 || len(input.After) > 4096 {
			http.Error(w, "invalid discovery request", 400)
			return
		}
		result, err := teams(ctx, strings.TrimSpace(input.APIKey), input.After)
		if err != nil {
			http.Error(w, "could not check API key or retrieve teams", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
