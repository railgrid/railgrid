/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"net/http"
	"os"
	"strings"
)

// identity is what each tool handler closes over so it can act on the caller's
// behalf. tenantPath/clusterID/user come from the headers the hub backend proxy
// injects after auth; token is the caller's own bearer token. Every kcp action
// runs as this token — there is no provider-wide identity.
//
// clusterID (X-Kedge-Cluster) is the workspace's kcp logical-cluster ID. kcp
// MUST be addressed by ID (/clusters/<id>), never by the workspace path: the hub
// proxy's membership gate rejects path-form /clusters/<root:...> with a 403.
// tenantPath stays for non-addressing uses (e.g. the transient commit-bundle
// scope, which is re-keyed to the cluster ID before the controller reads it).
type identity struct {
	tenantPath string
	clusterID  string
	user       string
	token      string
}

func identityFromRequest(r *http.Request) identity {
	id := identity{
		tenantPath: r.Header.Get("X-Kedge-Tenant"),
		clusterID:  r.Header.Get("X-Kedge-Cluster"),
		user:       r.Header.Get("X-Kedge-User"),
		token:      bearerToken(r),
	}
	if os.Getenv("KEDGE_DEV_ALLOW_TENANT_QUERY") == "true" {
		if id.tenantPath == "" {
			id.tenantPath = r.URL.Query().Get("tenant")
		}
		if id.clusterID == "" {
			id.clusterID = r.URL.Query().Get("cluster")
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

func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
