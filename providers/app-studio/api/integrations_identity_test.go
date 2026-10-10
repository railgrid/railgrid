/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/workloadidentity"
)

func TestProjectWorkloadIdentityMatchesCurrentProjectBindings(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid-a")},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{
			{
				Name: "development",
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "dev", Provider: "infrastructure", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{Name: "demo-dev", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances"},
					Values:      runtime.RawExtension{Raw: []byte(`{"name":"demo-dev"}`)},
				}},
			},
			{
				Name: "production",
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "production", Provider: "infrastructure", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{Name: "demo-prod", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances"},
				}},
			},
		}},
	}
	id := identity{clusterID: "cluster-a", workspacePath: "root:railgrid:tenants:org:workspace"}

	for _, scope := range []struct{ environment, instance string }{{"development", "demo-dev"}, {"production", "demo-prod"}} {
		caller := projectWorkloadIdentityCaller(id, project, scope.environment, scope.instance)
		id.caller = caller
		if !isProjectWorkloadIdentityCaller(caller) {
			t.Fatalf("caller %q was not recognized as a Project workload identity", caller.User)
		}
		if !projectWorkloadIdentityMatchesCurrentProject(id, project) {
			t.Errorf("current %s instance identity was rejected", scope.environment)
		}
	}
}

func TestProjectWorkloadIdentityRejectsReusedProjectAndStaleBinding(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid-new")},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Name: "development",
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
				Name: "dev", Provider: "infrastructure", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
				ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{Name: "demo-dev", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances"},
			}},
		}}},
	}
	id := identity{clusterID: "cluster-a", workspacePath: "root:railgrid:tenants:org:workspace"}
	oldProject := project.DeepCopy()
	oldProject.UID = types.UID("project-uid-old")
	id.caller = projectWorkloadIdentityCaller(id, oldProject, "development", "demo-dev")
	if projectWorkloadIdentityMatchesCurrentProject(id, project) {
		t.Fatal("identity from the previous Project UID matched a recreated Project")
	}

	id.caller = projectWorkloadIdentityCaller(id, project, "development", "demo-dev")
	project.Spec.Environments[0].Bindings[0].ResourceRef.Name = "demo-dev-replaced"
	if projectWorkloadIdentityMatchesCurrentProject(id, project) {
		t.Fatal("identity for a replaced runtime binding matched current Project state")
	}
}

func TestProjectWorkloadIdentityRequiresKCPClusterExtra(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Name: "development",
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
				Name: "dev", Provider: "infrastructure", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
				ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{Name: "demo-dev", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances"},
			}},
		}}},
	}
	id := identity{clusterID: "cluster-a", workspacePath: "root:railgrid:tenants:org:workspace"}
	id.caller = projectWorkloadIdentityCaller(id, project, "development", "demo-dev")
	id.caller.Extra[dataplane.ClusterNameExtra] = []string{"cluster-b"}
	if projectWorkloadIdentityMatchesCurrentProject(id, project) {
		t.Fatal("identity stamped for a different workspace cluster was accepted")
	}
}

func TestIsProjectWorkloadIdentityCallerLeavesHumanAndOrdinaryServiceAccountsAlone(t *testing.T) {
	for _, user := range []string{"alice@example.com", "system:serviceaccount:default:app"} {
		if isProjectWorkloadIdentityCaller(&dataplane.ProxiedIdentity{User: user}) {
			t.Errorf("ordinary caller %q was classified as a Project workload identity", user)
		}
	}
	if !isProjectWorkloadIdentityCaller(&dataplane.ProxiedIdentity{User: "system:serviceaccount:other:" + workloadidentity.ServiceAccountName(workloadidentity.Scope{})}) {
		t.Fatal("railgrid-wi identity in a non-default namespace was not fenced")
	}
}

func projectWorkloadIdentityCaller(id identity, project *aiv1alpha1.Project, environment, instance string) *dataplane.ProxiedIdentity {
	name := workloadidentity.ServiceAccountName(workloadidentity.Scope{
		TenantPath: id.workspacePath, Project: project.Name, ProjectUID: string(project.UID),
		Environment: environment, Instance: instance,
	})
	return &dataplane.ProxiedIdentity{
		User:  "system:serviceaccount:default:" + name,
		Extra: map[string][]string{dataplane.ClusterNameExtra: {id.clusterID}},
	}
}
