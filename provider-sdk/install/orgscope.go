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

package install

import (
	"context"
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// IsOrganizationProvider lets providers with additional bootstrap steps choose
// hub-mediated declarations before attempting privileged local API writes.
// Scope is resolved from kcp, never inferred from environment variables.
func IsOrganizationProvider(ctx context.Context, config *rest.Config) (bool, error) {
	if config == nil {
		return false, fmt.Errorf("install: Config is required")
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return false, err
	}
	path, err := workspacePathOf(ctx, client)
	if err != nil {
		return false, err
	}
	return isOrgOwnedWorkspace(path), nil
}
