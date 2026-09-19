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

package dataplane

import (
	"net/http"
	"strings"
)

// identity is the caller on whose behalf a data-plane request runs. token is
// the caller's own bearer token (forwarded as-is by the hub backend proxy);
// every runtime action is gated by the caller's RBAC on the instance, so there
// is no provider-wide identity. Mirrors mcpserver.identityFromRequest but kept
// local to avoid a package dependency.
//
// tenant is the hub's tenant identity for the request (X-Railgrid-Tenant: the
// workspace's kcp logical-cluster ID). The data plane addresses the workspace
// by the cluster ID in the URL and treats this value as opaque.
type identity struct {
	tenant string
	user   string
	token  string
}

func identityFromRequest(r *http.Request) identity {
	return identity{
		tenant: r.Header.Get("X-Railgrid-Tenant"),
		user:   r.Header.Get("X-Railgrid-User"),
		token:  bearerToken(r),
	}
}

// bearerToken extracts the caller's token from the Authorization header.
func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
