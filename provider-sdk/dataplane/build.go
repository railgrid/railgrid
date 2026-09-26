// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"errors"
	"net/url"
	"strings"
)

// ErrInvalidRequest is returned by the path builders for a Request that would
// not parse back to itself.
var ErrInvalidRequest = errors.New("dataplane: request does not render to a valid route")

// SubresourcePath renders the Request as the path a caller uses for a verb:
// the custom subresource kcp serves on the provider's APIExport,
//
//	/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail}][?component={component}]
//
// relative to whichever kcp front door the caller holds a credential for —
// the hub's /clusters/{id} for a user or a tenant ServiceAccount, or a
// provider's own export virtual workspace for a claimed verb on another
// provider's kind (Callers.ExportVerbURL). Consumers use it instead of
// string-building another provider's URL, so the grammar lives in one place.
//
// Version is ignored: an action's contract version is not part of the path
// (the serving provider restores it from its declaration). The component
// travels as a query parameter because kcp reads "{name}/{subresource}" and
// treats everything after the verb as the verb's own tail. The result always
// parses back to the same coordinates with ParseSubresourceRequest.
func SubresourcePath(group, version string, r Request) (string, error) {
	if !validSegment(group) || !validSegment(version) || !IsClusterID(r.ClusterID) {
		return "", ErrInvalidRequest
	}
	if !validSegment(r.Resource) || !validSegment(r.Name) || !validSegment(r.Verb) {
		return "", ErrInvalidRequest
	}
	if r.Verb == "status" || r.Verb == "scale" {
		return "", ErrInvalidRequest
	}
	parts := []string{"", "clusters", r.ClusterID, apisSegment, group, version, r.Resource, r.Name, r.Verb}
	if r.Tail != "" {
		for _, seg := range strings.Split(r.Tail, "/") {
			if !validSegment(seg) {
				return "", ErrInvalidRequest
			}
		}
		parts = append(parts, r.Tail)
	}
	path := strings.Join(parts, "/")
	if r.Component != "" {
		if !validSegment(r.Component) {
			return "", ErrInvalidRequest
		}
		path += "?" + ComponentQuery + "=" + url.QueryEscape(r.Component)
	}
	return path, nil
}

// SubresourceURL is SubresourcePath against a front-door base URL, e.g. the
// hub's origin or an export virtual-workspace endpoint. base may already end
// in a slash.
func SubresourceURL(base, group, version string, r Request) (string, error) {
	path, err := SubresourcePath(group, version, r)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + path, nil
}
