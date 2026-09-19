// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"net/http"
	"strings"
)

// The hub strips inbound X-Railgrid-* headers and re-injects these on every
// proxied request (pkg/hub/providers/proxy.go). They are addressing and
// labelling material only: authorization comes from the bearer alone.
const (
	// HeaderCluster carries the tenant workspace's kcp logical-cluster ID.
	HeaderCluster = "X-Railgrid-Cluster"
	// HeaderTenant is the older spelling of HeaderCluster. The hub sets both;
	// on some routes it holds a workspace path instead of a cluster ID.
	HeaderTenant = "X-Railgrid-Tenant"
	// HeaderUser is the authenticated caller's user name, for labels and logs.
	HeaderUser = "X-Railgrid-User"
)

// Identity extracts the caller from a proxied data-plane request.
//
// bearer comes from the Authorization header and nowhere else: there is no
// query-string fallback, so no dev bypass can ship in a release binary and no
// token lands in an access log. Missing or malformed credentials are
// ErrNoBearer.
//
// headerCluster is the hub's idea of which workspace this request addresses.
// It exists only for Gate's consistency check against the cluster in the
// path — the path wins, and nothing may be authorized from a header.
// X-Railgrid-Tenant is consulted only when X-Railgrid-Cluster is absent, and
// only when it holds a cluster ID: a workspace path there is not an identity
// this package can compare, so it is ignored rather than turned into a
// spurious mismatch.
//
// user is X-Railgrid-User verbatim, for labels and audit lines. It is never a
// trust root.
func Identity(r *http.Request) (bearer, headerCluster, user string, err error) {
	if r == nil {
		return "", "", "", ErrNoBearer
	}
	user = strings.TrimSpace(r.Header.Get(HeaderUser))

	if v := strings.TrimSpace(r.Header.Get(HeaderCluster)); v != "" {
		headerCluster = v
	} else if v := strings.TrimSpace(r.Header.Get(HeaderTenant)); IsClusterID(v) {
		headerCluster = v
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", headerCluster, user, ErrNoBearer
	}
	if token = strings.TrimSpace(token); token == "" {
		return "", headerCluster, user, ErrNoBearer
	}
	return token, headerCluster, user, nil
}
