/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import (
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMapGraphQLErrorMapsStandardModifiedObjectToConflict(t *testing.T) {
	err := mapGraphQLError([]gqlError{{Message: "Operation cannot be fulfilled on applications.infrastructure.kedge.faros.sh \\\"demo\\\": the object has been modified; please apply your changes to the latest version and try again"}}, &Resource{
		GVR:  schema.GroupVersionResource{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications"},
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
