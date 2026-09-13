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
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Team discovery is administrative and transient. Ordinary issue consumers see
// registered Teams; discovery does not create an Operation or expose a key.
func (s Server) teamDiscovery() http.Handler {
	return newTeamDiscoveryHandler(s.Caller, func(ctx context.Context, cluster, name string) (api.Connection, string, error) {
		client, err := s.Authority.Tenant(ctx, cluster, name)
		if err != nil {
			return api.Connection{}, "", err
		}
		return (engine.Engine{Client: client}).Connection(ctx, name)
	}, func(ctx context.Context, key, after string) (linearapi.Page[linearapi.Team], error) {
		return linearapi.New(key).Teams(ctx, 50, after)
	})
}

func newTeamDiscoveryHandler(callerFor func(*http.Request) (dynamic.Interface, error), resolve func(context.Context, string, string) (api.Connection, string, error), discover func(context.Context, string, string) (linearapi.Page[linearapi.Team], error)) http.Handler {
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		caller, err := callerFor(r)
		if err != nil {
			http.Error(w, "identity required", http.StatusUnauthorized)
			return
		}
		review, err := caller.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"group": "linear.providers.faros.sh", "resource": "teams", "verb": "create"}},
		}}, metav1.CreateOptions{})
		if err != nil {
			http.Error(w, "team registration permission required", http.StatusForbidden)
			return
		}
		allowed, _, _ := unstructured.NestedBool(review.Object, "status", "allowed")
		if !allowed {
			http.Error(w, "team registration permission required", http.StatusForbidden)
			return
		}
		name := r.PathValue("connection")
		visible, err := caller.Resource(engine.Connections).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			http.Error(w, "connection unavailable", http.StatusForbidden)
			return
		}
		conn, key, err := resolve(ctx, r.Header.Get("X-Faros-Cluster"), name)
		if err != nil || string(conn.UID) != string(visible.GetUID()) {
			http.Error(w, "connection changed or credential unavailable", http.StatusConflict)
			return
		}
		after := r.URL.Query().Get("after")
		if len(after) > 4096 {
			http.Error(w, "cursor too long", 400)
			return
		}
		result, err := discover(ctx, key, after)
		if err != nil {
			http.Error(w, "teams unavailable from Linear", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
