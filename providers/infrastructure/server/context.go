// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"net/http"
	"os"
)

// Headers injected by the hub backend proxy after PR #X
// (pkg/hub/providers/proxy.go → SetTenantResolver). The provider trusts
// them blindly — by the time a request lands here it has already
// passed through the hub's auth + the resolver, and inbound
// X-Kedge-* headers were stripped by the proxy before re-injection
// (defense in depth lives in the proxy, not duplicated here).
const (
	headerKedgeUser   = "X-Kedge-User"
	headerKedgeTenant = "X-Kedge-Tenant"
)

// tenantFromRequest extracts the tenant workspace path from
// X-Kedge-Tenant. In dev, if KEDGE_DEV_ALLOW_TENANT_QUERY=true, falls
// back to a ?tenant= query param so curl-only smoke tests work
// without standing up the full auth chain.
func tenantFromRequest(r *http.Request) string {
	if v := r.Header.Get(headerKedgeTenant); v != "" {
		return v
	}
	if os.Getenv("KEDGE_DEV_ALLOW_TENANT_QUERY") == "true" {
		return r.URL.Query().Get("tenant")
	}
	return ""
}

func userFromRequest(r *http.Request) string {
	if v := r.Header.Get(headerKedgeUser); v != "" {
		return v
	}
	if os.Getenv("KEDGE_DEV_ALLOW_TENANT_QUERY") == "true" {
		return r.URL.Query().Get("user")
	}
	return ""
}
