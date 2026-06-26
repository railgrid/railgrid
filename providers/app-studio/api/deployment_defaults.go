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
	"strconv"
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

func previewHTTPRouteEnabled() bool {
	return previewHTTPRouteBaseDomain() != "" && previewHTTPRouteParentGatewayName() != ""
}

func previewHTTPRouteBaseDomain() string {
	return envValue("APP_STUDIO_PREVIEW_BASE_DOMAIN")
}

func previewHTTPRouteParentGatewayName() string {
	return envValue("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAME")
}

func previewHTTPRouteParentGatewayNamespace() string {
	if value := envValue("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAMESPACE"); value != "" {
		return value
	}
	return "kedge-preview"
}

func previewHTTPRouteParentGatewaySectionName() string {
	if value := envValue("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_SECTION_NAME"); value != "" {
		return value
	}
	return "https"
}

func previewBackendNamespace() string {
	if value := envValue("APP_STUDIO_PREVIEW_BACKEND_NAMESPACE"); value != "" {
		return value
	}
	return "kedge-preview"
}

func previewBackendServiceName() string {
	if value := envValue("APP_STUDIO_PREVIEW_BACKEND_SERVICE_NAME"); value != "" {
		return value
	}
	return "sandbox-preview-gateway"
}

func previewBackendServicePort() int64 {
	value := envValue("APP_STUDIO_PREVIEW_BACKEND_SERVICE_PORT")
	if value == "" {
		return 8080
	}
	port, err := strconv.ParseInt(value, 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return 8080
	}
	return port
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
