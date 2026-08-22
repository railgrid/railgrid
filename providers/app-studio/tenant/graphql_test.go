/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMapGraphQLErrorMapsStandardModifiedObjectToConflict(t *testing.T) {
	err := mapGraphQLError([]gqlError{{Message: "Operation cannot be fulfilled on applications.infrastructure.faros.sh \\\"demo\\\": the object has been modified; please apply your changes to the latest version and try again"}}, &Resource{
		GVR:  schema.GroupVersionResource{Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "applications"},
		Kind: "Application",
	}, "demo")
	if !apierrors.IsConflict(err) {
		t.Fatalf("mapGraphQLError = %v, want Conflict", err)
	}
}

func TestMapGraphQLErrorMapsStandardNotFoundAndAlreadyExists(t *testing.T) {
	res := &Resource{GVR: schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}, Kind: "Widget"}
	for _, tc := range []struct {
		name string
		msg  string
		want func(error) bool
	}{
		{name: "not found", msg: "could not find the requested resource", want: apierrors.IsNotFound},
		{name: "already exists", msg: "widget \\\"demo\\\" already exists", want: apierrors.IsAlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := mapGraphQLError([]gqlError{{Message: tc.msg}}, res, "demo"); !tc.want(err) {
				t.Fatalf("mapGraphQLError(%q) = %v", tc.msg, err)
			}
		})
	}
}

func TestListInfrastructureInstancesUsesStableTypedFieldAndPreservesMetadata(t *testing.T) {
	const selector = "faros.sh/app-studio-run-sandbox=true"
	graphQL := &GraphQLClient{hubBase: "http://sandbox.test", http: &http.Client{Transport: graphqlRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if strings.Contains(payload.Query, "InstancesYaml") || !strings.Contains(payload.Query, "Instances(labelselector: $labelSelector)") {
			t.Fatalf("query = %q, want stable Instances field with label selector", payload.Query)
		}
		if got := payload.Variables["labelSelector"]; got != selector {
			t.Fatalf("labelSelector variable = %#v, want %q", got, selector)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":{"infrastructure_faros_sh":{"v1alpha1":{"Instances":{"items":[{"metadata":{"name":"sandbox-a","deletionTimestamp":null,"labels":{"faros.sh/app-studio-run-sandbox":"true"},"annotations":{"faros.sh/app-studio-run-sandbox-hard-expires-at":"2099-01-01T00:00:00Z"}},"status":{"phase":"Ready"}}]}}}}}`)),
			Request:    req,
		}, nil
	})}}
	scope, err := graphQL.For("cluster-id", "caller-token")
	if err != nil {
		t.Fatalf("create GraphQL scope: %v", err)
	}
	got, err := scope.ListInfrastructureInstances(context.Background(), metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list infrastructure instances: %v", err)
	}
	if len(got) != 1 || got[0].GetName() != "sandbox-a" {
		t.Fatalf("instances = %#v, want sandbox-a", got)
	}
	if got[0].GetAnnotations()["faros.sh/app-studio-run-sandbox-hard-expires-at"] == "" {
		t.Fatalf("instance annotations = %#v, want expiry annotation", got[0].GetAnnotations())
	}
}

type graphqlRoundTripFunc func(*http.Request) (*http.Response, error)

func (f graphqlRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
