/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workloadidentity

import "testing"

func TestServiceAccountNameIsStableAndScopeBound(t *testing.T) {
	scope := Scope{
		TenantPath: "root:railgrid:tenants:org:workspace",
		Project:    "project", ProjectUID: "project-uid", Environment: "development", Instance: "project-dev",
	}
	want := "railgrid-wi-1b56c9727031d066b9b876c0823598cb54ca1519"
	first := ServiceAccountName(scope)
	if first != want || first != ServiceAccountName(scope) {
		t.Fatalf("ServiceAccountName = %q, want stable %q", first, want)
	}
	if len(first) != len("railgrid-wi-")+40 {
		t.Fatalf("ServiceAccountName = %q, want prefix plus 40 hex characters", first)
	}
	for name, mutate := range map[string]func(*Scope){
		"tenant":      func(s *Scope) { s.TenantPath += ":other" },
		"project":     func(s *Scope) { s.Project += "-other" },
		"project UID": func(s *Scope) { s.ProjectUID += "-other" },
		"environment": func(s *Scope) { s.Environment += "-other" },
		"instance":    func(s *Scope) { s.Instance += "-other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := scope
			mutate(&changed)
			if got := ServiceAccountName(changed); got == first {
				t.Errorf("ServiceAccountName ignored changed %s", name)
			}
		})
	}
}

func TestIsServiceAccountName(t *testing.T) {
	if !IsServiceAccountName("railgrid-wi-0123456789abcdef0123456789abcdef01234567") {
		t.Fatal("hub-managed ServiceAccount name was not recognized")
	}
	if IsServiceAccountName("app") {
		t.Fatal("ordinary ServiceAccount name was recognized as hub-managed")
	}
}
