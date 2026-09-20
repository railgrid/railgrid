/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package hubapi holds the routes App Studio CONSUMES on the hub's own REST
// API — the provider catalog, and the org/workspace membership rosters it
// checks a share against with the delegated token the hub hands it
// (docs/providers.md, "Hub access").
//
// They live here, and not beside the handlers, because "a route this provider
// serves" and "a route this provider calls" are different things that happen
// to look alike. The provider serves exactly one shape — a data-plane verb on
// a bound resource — and hack/verify-provider-contract.mjs enforces that by
// reading main.go, server/ and api/. A hub URL sitting in api/ reads to that
// check, and to a person, as a route being served.
package hubapi

import "net/url"

// ProviderCatalogPath lists the providers enabled for the caller's workspace.
const ProviderCatalogPath = "/api" + "/providers"

// OrgMembershipsPath is the org roster: who may be shared with, and where a
// new member is invited.
func OrgMembershipsPath(orgUUID string) string {
	return "/api" + "/orgs/" + url.PathEscape(orgUUID) + "/memberships"
}

// WorkspaceMembershipsPath is the workspace roster.
func WorkspaceMembershipsPath(orgUUID, workspaceUUID string) string {
	return "/api" + "/orgs/" + url.PathEscape(orgUUID) + "/workspaces/" + url.PathEscape(workspaceUUID) + "/memberships"
}
