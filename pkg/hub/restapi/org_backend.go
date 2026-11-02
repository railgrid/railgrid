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

package restapi

import (
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"net/http"
)

func membershipIndexCovers(index *tenancyv1alpha1.UserMembershipIndex, orgUUID, wsUUID string) bool {
	if index == nil {
		return false
	}
	orgAdmin := false
	for _, entry := range index.Spec.Entries {
		if entry.OrgUUID != orgUUID || entry.SoftDeletedAt != nil || !validMembershipRole(entry.Role) {
			continue
		}
		if entry.WorkspaceUUID == wsUUID {
			return true
		}
		if entry.WorkspaceUUID == "" && entry.Role == tenancyv1alpha1.MembershipRoleAdmin {
			orgAdmin = true
		}
	}
	return orgAdmin
}

func validMembershipRole(role string) bool {
	return role == tenancyv1alpha1.MembershipRoleAdmin || role == tenancyv1alpha1.MembershipRoleMember
}

func orgBackendRouteAllowed(route sandboxRoute, method string) bool {
	if route.group == "rbac.authorization.k8s.io" || route.group == "authentication.k8s.io" || route.group == "authorization.k8s.io" {
		return false
	}
	if route.group == "" && (route.resource == "serviceaccounts" || (route.resource == "secrets" && route.subresource != "")) {
		return false
	}
	if route.resource == "namespaces" && method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if route.resource == "resourcequotas" && method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if route.group == "apis.kcp.io" || (route.group == "core.kcp.io" && route.resource == "logicalclusters") {
		return false
	}
	if route.group == "apiextensions.k8s.io" && route.resource == "customresourcedefinitions" {
		return false
	}
	if route.group == "tenancy.kcp.io" {
		return false
	}
	if route.subresource == "impersonate" || route.subresource == "token" || route.resource == "tokenreviews" || route.resource == "subjectaccessreviews" {
		return false
	}
	return true
}
