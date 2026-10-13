/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package railgridauth validates the declared Infrastructure template wiring
// used by App Studio's Railgrid Auth integration.
package railgridauth

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

const (
	canonicalRailgridAuthToggle = `${schema.spec.railgridAuth ? "true" : "false"}`
	canonicalAuthIssuer         = `${schema.spec.railgridAuthAppID != "" && schema.spec.railgridAuthInstanceUID != "" ? "${railgrid.hubPublicUrl}/auth/apps/identity" : ""}`
	canonicalAuthAudience       = `${schema.spec.railgridAuthAppID != "" && schema.spec.railgridAuthInstanceUID != "" ? schema.spec.railgridAuthAppID : ""}`
	canonicalAuthEnvironment    = `${schema.spec.railgridAuthAppID != "" && schema.spec.railgridAuthInstanceUID != "" ? schema.spec.railgridAuthEnvironment : ""}`
	canonicalAuthOrigin         = `${schema.spec.railgridAuthAppID != "" && schema.spec.railgridAuthInstanceUID != "" ? "https://" + schema.spec.expose.fqdn + "${railgrid.appPublicPort}" : ""}`
	canonicalAuthInstanceUID    = `${schema.spec.railgridAuthAppID != "" && schema.spec.railgridAuthInstanceUID != "" ? schema.spec.railgridAuthInstanceUID : ""}`
	accessProxyImageToken       = `${railgrid.accessProxyImage}`
)

// Validate checks the declared field/development contract and confirms that
// the KRO graph uses the canonical v1 expressions to connect the opt-in flag
// and bound application identity values to the access gate and API workload.
// It validates integration wiring only; it cannot establish that app code
// authorizes access to protected data.
func Validate(tmpl *infrav1alpha1.Template) error {
	if tmpl == nil || tmpl.Spec.RailgridAuth == nil {
		return nil
	}
	if err := tmpl.Spec.ValidateRailgridAuth(); err != nil {
		return err
	}
	if tmpl.Spec.Backend != "kro" {
		return fmt.Errorf("spec.railgridAuth currently requires spec.backend=kro so the controller can inspect its workload graph")
	}
	resources, err := backendResources(tmpl.Spec.BackendConfig)
	if err != nil {
		return err
	}
	var gateWired, apiWorkloadFound, apiWired bool
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := resource["id"].(string)
		template, ok := resource["template"].(map[string]any)
		if !ok {
			continue
		}
		apiVersion, _ := template["apiVersion"].(string)
		kind, _ := template["kind"].(string)
		podSpec, _ := nestedMap(template, "spec", "template", "spec")
		containers, _ := podSpec["containers"].([]any)
		isAPIWorkload := id == "api" || id == "apiDeployment"
		if isAPIWorkload && apiVersion == "apps/v1" && kind == "Deployment" {
			apiWorkloadFound = true
		}
		for _, rawContainer := range containers {
			container, ok := rawContainer.(map[string]any)
			if !ok {
				continue
			}
			env := containerEnv(container)
			if apiVersion == "apps/v1" && kind == "Deployment" && isAccessProxy(container, env) && hasCanonicalAuthEnv(env, true) {
				gateWired = true
			}
			if isAPIWorkload && apiVersion == "apps/v1" && kind == "Deployment" && hasCanonicalAuthEnv(env, false) {
				apiWired = true
			}
		}
	}
	if !gateWired {
		return fmt.Errorf("spec.railgridAuth v1 requires the canonical KRO access-proxy Deployment and Railgrid Auth env expressions")
	}
	if !apiWorkloadFound || !apiWired {
		return fmt.Errorf("spec.railgridAuth v1 requires the api development component's KRO Deployment (id api or apiDeployment) with canonical issuer, origin, application ID, and instance UID env expressions")
	}
	return nil
}

func backendResources(rawExtension *runtime.RawExtension) ([]any, error) {
	if rawExtension == nil {
		return nil, fmt.Errorf("spec.railgridAuth requires backendConfig resource graph wiring")
	}
	raw := rawExtension.Raw
	if len(raw) == 0 && rawExtension.Object != nil {
		var err error
		raw, err = json.Marshal(rawExtension.Object)
		if err != nil {
			return nil, fmt.Errorf("spec.railgridAuth decode backendConfig: %w", err)
		}
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("spec.railgridAuth decode backendConfig: %w", err)
	}
	resources, ok := config["resources"].([]any)
	if !ok || len(resources) == 0 {
		return nil, fmt.Errorf("spec.railgridAuth requires backendConfig.resources")
	}
	return resources, nil
}

func nestedMap(object map[string]any, fields ...string) (map[string]any, bool) {
	current := object
	for _, field := range fields {
		next, ok := current[field].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func containerEnv(container map[string]any) map[string]string {
	out := map[string]string{}
	entries, _ := container["env"].([]any)
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		if name != "" {
			out[name] = value
		}
	}
	return out
}

func isAccessProxy(container map[string]any, env map[string]string) bool {
	return container["image"] == accessProxyImageToken &&
		nonEmpty(env["RAILGRID_ACCESS_PROXY_ROUTES"]) &&
		env["RAILGRID_ACCESS_PROXY_INSTANCE_GROUP"] == "infrastructure.railgrid.ai" &&
		env["RAILGRID_ACCESS_PROXY_INSTANCE_RESOURCE"] == "instances"
}

func hasCanonicalAuthEnv(env map[string]string, includeGateFields bool) bool {
	if env["RAILGRID_AUTH_ISSUER"] != canonicalAuthIssuer ||
		env["RAILGRID_AUTH_AUDIENCE"] != canonicalAuthAudience ||
		env["RAILGRID_AUTH_ORIGIN"] != canonicalAuthOrigin ||
		env["RAILGRID_AUTH_INSTANCE_UID"] != canonicalAuthInstanceUID {
		return false
	}
	return !includeGateFields ||
		(env["RAILGRID_ACCESS_PROXY_RAILGRID_AUTH"] == canonicalRailgridAuthToggle &&
			env["RAILGRID_AUTH_ENVIRONMENT"] == canonicalAuthEnvironment)
}

func nonEmpty(value string) bool {
	return strings.TrimSpace(value) != ""
}
