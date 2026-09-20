/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"net/http"
	"strings"

	"github.com/railgrid/provider-sdk/dataplane"
)

// identity is what each tool handler closes over so it can act on the caller's
// behalf. token is the caller's own bearer — every kcp action runs as it, and
// there is no provider-wide identity and no query-string fallback, so no dev
// bypass can ship in a release binary or leave a token in an access log.
//
// clusterID (X-Railgrid-Cluster) is the workspace's kcp logical-cluster ID,
// injected by the hub backend proxy after it authenticates the request. kcp
// MUST be addressed by ID (/clusters/<id>), never by a workspace path: the hub
// proxy's membership gate rejects path-form /clusters/<root:...> with a 403.
// It is also the scope key for the transient commit-bundle staging store, the
// same key the RepositoryCommit controller reconciles under (req.ClusterName).
// user (X-Railgrid-User) is for labels and logs only; it is never a trust root.
type identity struct {
	clusterID string
	user      string
	token     string
}

func identityFromRequest(r *http.Request) identity {
	return identity{
		clusterID: strings.TrimSpace(r.Header.Get(dataplane.HeaderCluster)),
		user:      strings.TrimSpace(r.Header.Get(dataplane.HeaderUser)),
		token:     bearerToken(r),
	}
}

func bearerToken(r *http.Request) string {
	token, _, _, err := dataplane.Identity(r)
	if err != nil {
		return ""
	}
	return token
}
