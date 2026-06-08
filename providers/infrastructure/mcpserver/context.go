// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"net/http"
	"os"
	"strings"
)

// identity is what each tool handler closes over so it can act on the
// caller's behalf. tenantPath/user come from the headers the hub backend
// proxy injects after auth (see pkg/hub/providers/proxy.go); token is the
// caller's own bearer token (forwarded as-is by that proxy). Every kcp
// action runs as this token — there is no provider-wide identity.
type identity struct {
	tenantPath string
	user       string
	token      string
}

// identityFromRequest mirrors server/context.go's tenantFromRequest /
// userFromRequest helpers. Kept in this package rather than importing
// the http server package to avoid a Go cycle (mcpserver is used by
// the server package via Deps composition; the reverse direction is
// not needed).
func identityFromRequest(r *http.Request) identity {
	id := identity{
		tenantPath: r.Header.Get("X-Kedge-Tenant"),
		user:       r.Header.Get("X-Kedge-User"),
		token:      bearerToken(r),
	}
	if os.Getenv("KEDGE_DEV_ALLOW_TENANT_QUERY") == "true" {
		if id.tenantPath == "" {
			id.tenantPath = r.URL.Query().Get("tenant")
		}
		if id.user == "" {
			id.user = r.URL.Query().Get("user")
		}
		if id.token == "" {
			id.token = r.URL.Query().Get("token")
		}
	}
	return id
}

// bearerToken extracts the caller's token from the Authorization header.
// The hub backend proxy forwards Authorization as-is, so this is the same
// token the user/agent authenticated the MCP request with.
func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
