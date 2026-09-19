/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import "testing"

func TestValidateProviderDataPlane(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verbs   []ProviderDataPlaneVerb
		wantErr bool
	}{
		{name: "nothing declared", verbs: nil},
		{
			name: "the real infrastructure surface",
			verbs: []ProviderDataPlaneVerb{
				{Resource: "instances", Verb: "exec", Stream: true},
				{Resource: "instances", Verb: "log", Stream: true, ReadOnly: true},
				{Resource: "instances", Verb: "proxy", Stream: true},
			},
		},
		{
			name:  "the same verb on two resources is fine",
			verbs: []ProviderDataPlaneVerb{{Resource: "kubernetesclusters", Verb: "proxy"}, {Resource: "services", Verb: "proxy"}},
		},
		{
			name:    "a duplicate coordinate is rejected",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances", Verb: "exec"}, {Resource: "instances", Verb: "exec"}},
			wantErr: true,
		},
		{
			// instances/get would read like the ordinary get on instances.
			name:    "a standard kube verb cannot be a data-plane verb",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances", Verb: "get"}},
			wantErr: true,
		},
		{
			name:    "a versioned verb is rejected: versions live in actions",
			verbs:   []ProviderDataPlaneVerb{{Resource: "tables", Verb: "query/v1"}},
			wantErr: true,
		},
		{
			name:    "an uppercase verb is rejected",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances", Verb: "Exec"}},
			wantErr: true,
		},
		{
			name:    "a subresource-shaped resource is rejected",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances/exec", Verb: "run"}},
			wantErr: true,
		},
		{
			name:    "an empty verb is rejected",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances", Verb: ""}},
			wantErr: true,
		},
		{
			name:    "a newline in a description is rejected",
			verbs:   []ProviderDataPlaneVerb{{Resource: "instances", Verb: "exec", Description: "line\nbreak"}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderDataPlane(&ProviderDataPlane{Verbs: tc.verbs})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
	if err := ValidateProviderDataPlane(nil); err != nil {
		t.Fatalf("a provider that declares no verbs must validate: %v", err)
	}
}

func TestProviderDataPlaneVerbsIsScopedToItsResource(t *testing.T) {
	dataPlane := &ProviderDataPlane{Verbs: []ProviderDataPlaneVerb{
		{Resource: "kubernetesclusters", Verb: "k8s"},
		{Resource: "kubernetesclusters", Verb: "ssh"},
		{Resource: "services", Verb: "proxy"},
	}}
	got := ProviderDataPlaneVerbs(dataPlane, "kubernetesclusters")
	if len(got) != 2 || got[0] != "k8s" || got[1] != "ssh" {
		t.Fatalf("verbs = %v", got)
	}
	if verbs := ProviderDataPlaneVerbs(dataPlane, "linuxservers"); len(verbs) != 0 {
		t.Fatalf("a verb declared on one resource leaked to another: %v", verbs)
	}
	if verbs := ProviderDataPlaneVerbs(nil, "instances"); len(verbs) != 0 {
		t.Fatalf("nil declaration returned %v", verbs)
	}
}
