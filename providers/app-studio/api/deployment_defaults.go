/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"encoding/json"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func defaultProjectSpec(projectName, displayName, description string, repository *aiv1alpha1.ProjectRepositoryBinding) aiv1alpha1.ProjectSpec {
	return aiv1alpha1.ProjectSpec{
		DisplayName:  displayName,
		Description:  description,
		Repository:   repository,
		Memory:       emptyProjectMemory(),
		Sharing:      privateProjectSharingSpec(),
		Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment(projectName)},
	}
}

func privateProjectSharingSpec() aiv1alpha1.ProjectSharingSpec {
	return aiv1alpha1.ProjectSharingSpec{
		Preview: aiv1alpha1.ProjectSharingPolicy{
			Mode: aiv1alpha1.ProjectSharingModePrivate,
		},
		Publishing: aiv1alpha1.ProjectSharingPolicy{
			Mode: aiv1alpha1.ProjectSharingModePrivate,
		},
	}
}

func defaultProjectDevelopmentEnvironment(projectName string) aiv1alpha1.ProjectEnvironmentSpec {
	return aiv1alpha1.ProjectEnvironmentSpec{
		Name:       "development",
		Mode:       aiv1alpha1.ProjectEnvironmentModeLive,
		AutoDeploy: false,
		Promotion:  aiv1alpha1.ProjectPromotionManual,
		Bindings:   []aiv1alpha1.ProjectProviderBindingSpec{defaultSandboxRunnerBinding(projectName)},
	}
}

func defaultSandboxRunnerBinding(projectName string) aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     "dev",
		Provider: "app-studio",
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
			Kind:       "SandboxRunner",
			Resource:   "sandboxrunners",
		},
		Values: projectDeploymentJSONValues(sandboxRunnerValues(projectName)),
	}
}

// sandboxRunnerValues are the project-scoped fields App Studio supplies on a
// SandboxRunner binding. The runner image is NOT one of them: the sandbox-runner
// template declares spec.runnerImage as a schema field with a sane default (the
// web-app convention), so App Studio doesn't pass it. See
// providers/infrastructure/docs/template-conventions.md.
func sandboxRunnerValues(projectName string) map[string]any {
	return map[string]any{
		"projectRef": projectName,
	}
}

// previewHTTPRouteBaseDomain is the public base domain used to validate (anti-
// spoof) the SandboxRunner's reported preview host: it must equal
// <runner>.<baseDomain>. The HTTPRoute itself is created by the infrastructure
// provider's SandboxRunner RGD (on the platform Gateway), so App Studio only
// needs the domain for this check — set APP_STUDIO_PREVIEW_BASE_DOMAIN to the
// same value as the infra provider's KEDGE_APP_BASE_DOMAIN.
func previewHTTPRouteBaseDomain() string {
	return envValue("APP_STUDIO_PREVIEW_BASE_DOMAIN")
}

func envValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func projectDeploymentJSONValues(values map[string]any) runtime.RawExtension {
	raw, _ := json.Marshal(values)
	return runtime.RawExtension{Raw: raw}
}
