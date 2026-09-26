// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package dataplane is the shared server-kit for provider data-plane verbs.
//
// A verb is a kcp custom subresource: the provider declares "{resource}/{verb}"
// on its APIExport, kcp authorizes a call with ordinary RBAC and reverse-proxies
// it to the provider with the caller's identity stamped in requestheader
// headers. This package is what a provider needs to serve that:
//
//	ParseSubresourceRequest — the one path grammar, with the component of a
//	                          multi-component object as a query parameter.
//	SubresourcePath         — the inverse, for a caller addressing a verb.
//	ProxiedCaller           — the identity the shard stamped.
//	Gate                    — visibility of the parent, decided on the caller's
//	                          behalf and read as the provider.
//	Authorize               — any further question about the caller.
//	Callers                 — the provider's clients: as itself through its
//	                          export virtual workspace, and (MCP only) as a
//	                          bearer-credentialed caller.
//	Serve                   — action input/output limits and the actionwire
//	                          envelope.
//	WriteError              — the status mapping, with no tenant detail in the
//	                          body.
//
// Nothing here imports a provider. See README.md for a wiring example.
package dataplane

import (
	"net/url"
	"regexp"
	"strings"
)

// maxSegment is the longest path segment accepted, matching the Kubernetes
// object-name limit.
const maxSegment = 253

// Request is the coordinates of one verb call:
//
//	/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail...}][?component={component}]
//
// ClusterID is the tenant workspace's kcp logical-cluster ID, never a
// workspace path. Component is the component of a multi-component object, or
// empty. Version is an action's contract version, restored by serve's adapter
// from the provider's declaration (it is not part of the path). Tail is the
// remaining path with no leading or trailing slash, for a verb that addresses
// something beneath itself (a proxy verb's upstream path).
type Request struct {
	ClusterID string
	Resource  string
	Name      string
	Component string
	Verb      string
	Version   string
	Tail      string
}

// clusterIDPattern is the shape of a kcp logical-cluster name: a lowercase
// DNS-label-like identifier (kcp mints 16-character base36 names; "root" is
// the root cluster). A workspace path contains ":" and never matches.
var clusterIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// IsClusterID reports whether s has the shape of a kcp logical-cluster ID.
func IsClusterID(s string) bool { return clusterIDPattern.MatchString(s) }

// validSegment reports whether s may stand as one path segment: non-empty,
// not a dot segment, bounded, and already percent-encoding-clean so a
// separator cannot be smuggled in encoded.
func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > maxSegment {
		return false
	}
	if strings.ContainsAny(s, "/\x00") {
		return false
	}
	return url.PathEscape(s) == s
}
