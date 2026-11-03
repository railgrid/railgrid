// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"strings"
)

// BoundCopyResolver selects a backend using the authenticated caller's verified
// workspace and its live APIBinding. It must never select from request headers
// or silently substitute the catalog's same-named organization copy.
type BoundCopyResolver func(ctx context.Context, tenantPath, cluster, name string) (Provider, error)

func (p *ProviderProxy) SetBoundCopyResolver(resolve BoundCopyResolver) {
	p.boundCopy = resolve
}

// BoundCopy matches the immutable coordinates of an actual APIBinding against
// a current registration visible to this organization. A same-named replacement
// with a new provider workspace is a different copy.
func (r *Registry) BoundCopy(org, name, path, export, cluster string) (Provider, bool) {
	if path == "" || export == "" || cluster == "" {
		return Provider{}, false
	}
	var selected Provider
	matched := false
	for _, entry := range r.List() {
		if entry.Name != name || (entry.OrgUUID != "" && entry.OrgUUID != org) || entry.APIExportPath != path || entry.APIExportName != export || entry.WorkspaceCluster != cluster {
			continue
		}
		if matched {
			return Provider{}, false
		}
		selected, matched = entry, true
	}
	return selected, matched
}

// Webhook bearers belong to the external sender, not the hub tenant. Agent
// ingress has its own credential resolver. Only tenant backend classes use
// this workspace binding lookup.
func usesBoundBackendCopy(path, prefix, name string) bool {
	rest := strings.TrimPrefix(path, prefix+"/"+name)
	return rest == "/mcp" || strings.HasPrefix(rest, "/mcp/") || rest == "/oauth" || strings.HasPrefix(rest, "/oauth/") || rest == "/healthz" || rest == "/readyz"
}
