// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"errors"
	"strings"
)

// ErrInvalidRequest is returned by Path when a Request would not round-trip
// through ParsePath: an empty or malformed cluster ID, resource, name, verb,
// component, version or tail segment.
var ErrInvalidRequest = errors.New("dataplane: request does not form a valid path")

// Path is the inverse of ParsePath: it renders the Request as the exact route
// the owning provider serves under root, e.g.
//
//	/dataplane/clusters/rgl3jcl2cfl3xa5p/instances/my-site/components/app/log
//	/actions/clusters/rgl3jcl2cfl3xa5p/repositories/api/branches/v1
//
// Consumers use it instead of string-building another provider's URL, so the
// grammar lives in one place (cross-provider-simplification X-8). The result
// always round-trips: ParsePath(root, r.Path(root)) == r.
func (r Request) Path(root string) (string, error) {
	root = strings.Trim(root, "/")
	if root == "" || !IsClusterID(r.ClusterID) || r.Resource == legacyAPIsSegment {
		return "", ErrInvalidRequest
	}
	if !validSegment(r.Resource) || !validSegment(r.Name) || !validSegment(r.Verb) || r.Verb == componentsSegment {
		return "", ErrInvalidRequest
	}
	parts := []string{"", root, "clusters", r.ClusterID, r.Resource, r.Name}
	if r.Component != "" {
		if !validSegment(r.Component) {
			return "", ErrInvalidRequest
		}
		parts = append(parts, componentsSegment, r.Component)
	}
	parts = append(parts, r.Verb)
	if root == ActionsRoot {
		if !validSegment(r.Version) {
			return "", ErrInvalidRequest
		}
		parts = append(parts, r.Version)
	} else if r.Version != "" {
		return "", ErrInvalidRequest
	}
	if r.Tail != "" {
		for _, seg := range strings.Split(r.Tail, "/") {
			if !validSegment(seg) {
				return "", ErrInvalidRequest
			}
		}
		parts = append(parts, r.Tail)
	}
	return strings.Join(parts, "/"), nil
}

// ProviderPath is Path prefixed with the hub's backend-proxy mount for the
// owning provider, i.e. the URL a consumer (another provider, a portal, an
// MCP tool) calls on the hub:
//
//	/services/providers/infrastructure/dataplane/clusters/{id}/instances/{n}/log
//
// The provider name comes from the consumer's binding to that provider's
// APIExport, never from configuration.
func ProviderPath(provider, root string, r Request) (string, error) {
	if !validSegment(provider) {
		return "", ErrInvalidRequest
	}
	p, err := r.Path(root)
	if err != nil {
		return "", err
	}
	return "/services/providers/" + provider + p, nil
}
