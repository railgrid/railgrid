// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package dataplane is the shared server-kit for provider data-plane verbs.
//
// A provider may serve exactly one REST shape for tenant traffic (the Pillar 2
// contract in docs/provider-contract-review.md): a verb on a bound resource,
// addressed by the tenant's kcp logical-cluster ID, authorized as the caller.
// This package owns that shape end to end so no provider writes its own path
// parser, its own gates or its own limit handling again:
//
//	ParsePath  — the one grammar, with every rejection the contract requires.
//	Identity   — the caller's bearer and the hub-injected addressing headers.
//	Gate       — the two gates (a real GET, then an SSAR) run as the caller.
//	Serve      — action input/output limits and the actionwire envelope.
//	WriteError — the status mapping, with no tenant detail in the body.
//
// Nothing here imports a provider. See README.md for a wiring example.
package dataplane

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ActionsRoot is the reserved root for provider actions. Under it the verb
// segment is "{action}/{version}" and Request.Version is set.
const ActionsRoot = "actions"

// DataplaneRoot is the conventional root for non-action data-plane verbs.
const DataplaneRoot = "dataplane"

// componentsSegment is reserved: it introduces the component form of the
// grammar and can therefore never be a verb in the plain form.
const componentsSegment = "components"

// legacyAPIsSegment is the resource-position segment of the edges dialect
// (".../clusters/{id}/apis/{group}/{version}/{resource}/..."). It is not part
// of the contract grammar; ParsePath refuses it so a caller that still uses it
// is routed by an explicit fallback rather than silently misparsed.
const legacyAPIsSegment = "apis"

// maxSegment is the longest path segment accepted, matching the Kubernetes
// object-name limit.
const maxSegment = 253

// Request is one parsed data-plane route:
//
//	/{root}/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail...}]
//	/{root}/clusters/{clusterID}/{resource}/{name}/components/{component}/{verb}[/{tail...}]
//
// ClusterID is the tenant workspace's kcp logical-cluster ID, never a
// workspace path: the hub proxy addresses kcp by /clusters/{id} and rejects
// the path form. Component is empty in the plain form. Version is set only
// under ActionsRoot, where the verb segment is "{action}/{version}" and Verb
// holds the action name alone. Tail is the remaining path with no leading or
// trailing slash, and is empty when the route has none.
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

// ParsePath parses path against the grammar rooted at root and reports
// whether it is a well-formed data-plane route. It never returns a partially
// filled Request: on false the zero value comes back and the caller must fall
// through to its own routing (404, or an explicit legacy handler).
//
// It refuses, in addition to anything that does not match the grammar:
//   - an empty segment, or a segment that is "." or ".." (including in Tail),
//     longer than 253 bytes, or that is not already percent-encoding-clean
//     (url.PathEscape(s) != s), so a caller cannot smuggle a separator;
//   - a cluster segment that is not a kcp logical-cluster ID — in particular
//     a workspace path, which the hub proxy will not serve;
//   - "components" as a verb in the plain form, and the component form with an
//     empty component or no verb after it;
//   - "apis" in the resource position: that is the legacy edges dialect, and
//     it is rejected here rather than reinterpreted.
func ParsePath(root, path string) (Request, bool) {
	root = strings.Trim(root, "/")
	if root == "" {
		return Request{}, false
	}
	rest, ok := strings.CutPrefix(path, "/"+root+"/")
	if !ok {
		return Request{}, false
	}
	rest, ok = strings.CutPrefix(rest, "clusters/")
	if !ok {
		return Request{}, false
	}
	// parts: [clusterID, resource, name, verb-and-tail]
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 4 {
		return Request{}, false
	}
	req := Request{ClusterID: parts[0], Resource: parts[1], Name: parts[2]}

	if trimmed, isComponent := strings.CutPrefix(parts[3], componentsSegment+"/"); isComponent {
		// seg: [component, verb, tail?]
		seg := strings.SplitN(trimmed, "/", 3)
		if len(seg) < 2 {
			return Request{}, false
		}
		req.Component, req.Verb = seg[0], seg[1]
		if len(seg) == 3 {
			req.Tail = seg[2]
		}
		if !validSegment(req.Component) {
			return Request{}, false
		}
	} else {
		seg := strings.SplitN(parts[3], "/", 2)
		req.Verb = seg[0]
		if len(seg) == 2 {
			req.Tail = seg[1]
		}
		if req.Verb == componentsSegment {
			return Request{}, false
		}
	}

	// Under the actions root the verb carries its contract version, which the
	// caller pins and the server matches; it is not part of the SSAR
	// subresource, so it is split out here.
	if root == ActionsRoot {
		version, tail, _ := strings.Cut(req.Tail, "/")
		req.Version, req.Tail = version, tail
		if !validSegment(req.Version) {
			return Request{}, false
		}
	}

	if !IsClusterID(req.ClusterID) || req.Resource == legacyAPIsSegment {
		return Request{}, false
	}
	if !validSegment(req.Resource) || !validSegment(req.Name) || !validSegment(req.Verb) {
		return Request{}, false
	}
	if req.Tail != "" {
		for _, seg := range strings.Split(req.Tail, "/") {
			if !validSegment(seg) {
				return Request{}, false
			}
		}
	}
	return req, true
}

// ParseRequest is ParsePath over an *http.Request. It additionally refuses a
// path that carried percent-encoded separators (r.URL.RawPath is set when the
// escaped form differs from the default encoding of the decoded path), so a
// caller cannot hide a "/" or a "%" inside a segment.
func ParseRequest(root string, r *http.Request) (Request, bool) {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return Request{}, false
	}
	return ParsePath(root, r.URL.Path)
}

// validSegment reports whether s is usable as one path segment.
func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > maxSegment {
		return false
	}
	if strings.ContainsAny(s, "/\x00") {
		return false
	}
	return url.PathEscape(s) == s
}
