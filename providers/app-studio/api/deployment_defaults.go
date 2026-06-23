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
		Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment(projectName)},
	}
}

func defaultProjectDevelopmentEnvironment(projectName string) aiv1alpha1.ProjectEnvironmentSpec {
	return aiv1alpha1.ProjectEnvironmentSpec{
		Name:       "development",
		Mode:       aiv1alpha1.ProjectEnvironmentModeLive,
		AutoDeploy: false,
		Promotion:  aiv1alpha1.ProjectPromotionManual,
		Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
			Name:     "dev",
			Provider: "app-studio",
			Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
			ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
				APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
				Kind:       "SandboxRunner",
				Resource:   "sandboxrunners",
			},
			Values: projectDeploymentJSONValues(sandboxRunnerValues(projectName)),
		}},
	}
}

func sandboxRunnerValues(projectName string) map[string]any {
	values := map[string]any{
		"projectRef": projectName,
	}
	if image := sandboxRunnerImage(); image != "" {
		values["runnerImage"] = image
	}
	if image := sandboxTokenGeneratorImage(); image != "" {
		values["tokenGeneratorImage"] = image
	}
	return values
}

func sandboxRunnerImage() string {
	return envValue("APP_STUDIO_SANDBOX_RUNNER_IMAGE")
}

func sandboxTokenGeneratorImage() string {
	return envValue("APP_STUDIO_SANDBOX_TOKEN_GENERATOR_IMAGE")
}

func envValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func projectDeploymentJSONValues(values map[string]any) runtime.RawExtension {
	raw, _ := json.Marshal(values)
	return runtime.RawExtension{Raw: raw}
}
