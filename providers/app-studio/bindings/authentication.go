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

package bindings

import (
	"encoding/json"
	"fmt"
	"strings"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

const AuthEnabledField = "railgridAuth"
const AuthAppIDField = "railgridAuthAppID"
const AuthEnvironmentField = "railgridAuthEnvironment"

// ApplyAuthentication derives reserved identity fields from the persisted owner,
// rather than accepting an app or environment identity supplied by a caller.
func ApplyAuthentication(p *aiv1alpha1.Project, environment string, binding aiv1alpha1.ProjectProviderBindingSpec) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	if p == nil || binding.Provider != "app-studio" || binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
		return binding, nil
	}
	values, err := Values(binding)
	if err != nil {
		return binding, err
	}
	// This field belongs to the Infrastructure controller, never the binding.
	_, hasInstanceUID := values["railgridAuthInstanceUID"]
	delete(values, "railgridAuthInstanceUID")
	_, configured := values[AuthEnabledField]
	if !p.Spec.Authentication.Enabled && !configured && !hasInstanceUID {
		return binding, nil
	}
	if !p.Spec.Authentication.Enabled && !configured {
		raw, err := json.Marshal(values)
		binding.Values.Raw = raw
		return binding, err
	}
	if p.Spec.Authentication.Enabled && (strings.TrimSpace(string(p.UID)) == "" || strings.TrimSpace(environment) == "") {
		return binding, fmt.Errorf("railgrid auth requires a persisted Project and environment")
	}
	values[AuthEnabledField] = p.Spec.Authentication.Enabled
	values[AuthAppIDField] = string(p.UID) + "/" + environment
	values[AuthEnvironmentField] = environment
	raw, err := json.Marshal(values)
	if err != nil {
		return binding, err
	}
	binding.Values.Raw = raw
	return binding, nil
}
