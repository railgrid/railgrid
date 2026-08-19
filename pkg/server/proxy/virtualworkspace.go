/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"

	"github.com/faroshq/faros/pkg/apiurl"
)

// APIExport virtual-workspace relaying.
//
// A provider watches every workspace that consumes its APIExport through kcp's
// virtual workspace, whose address kcp copies from Shard.spec.virtualWorkspaceURL
// into APIExportEndpointSlice.status.endpoints[].url. Point that at a service
// DNS name and only consumers inside the shard's own cluster can dial it — which
// is fine for platform providers and fatal for self-hosted ones, whose failure
// is quiet: Templates live in the provider's own workspace and keep reconciling
// over the provider kubeconfig, so the provider looks healthy while never acting
// on a single tenant Instance.
//
// Relaying here lets a single-shard or embedded platform publish its ONE public
// hub address as the virtual-workspace URL, so the operator exposes one
// entrypoint rather than one per shard.
//
// This does not generalize to multiple shards. A multi-shard
// APIExportEndpointSlice carries one endpoint per shard, each derived from that
// shard's own virtualWorkspaceURL, and nothing in the request distinguishes
// them — the cluster segment identifies the APIExport's workspace, not the shard
// serving it. A SaaS deployment must give each shard its own reachable URL.
// See docs/byo-providers.md.

// virtualWorkspaceRequest is a parsed APIExport virtual-workspace URL:
//
//	/services/apiexport/{cluster}/{export}/clusters/{wildcard}/apis/...
//	                    ^^^^^^^^^ ^^^^^^^^
//
// Cluster is the logical cluster holding the APIExport — NOT the workspace
// being read through it, which appears later in the path and is kcp's to
// authorize.
type virtualWorkspaceRequest struct {
	Cluster string
	Export  string
}

// clusterSegmentRE matches a kcp logical cluster id or a colon-separated
// workspace path. Same shape serveServiceAccount validates the clusterName
// claim against, so a token can never match a segment it could not also name.
var clusterSegmentRE = regexp.MustCompile(`^[a-z0-9]+(?:[:-][a-z0-9]+)*$`)

// exportSegmentRE matches an APIExport name (a DNS subdomain, e.g.
// "infrastructure.providers.faros.sh").
var exportSegmentRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// parseVirtualWorkspacePath reports whether urlPath addresses an APIExport
// virtual workspace, and if so returns its cluster and export.
//
// It returns false for anything malformed rather than a partial parse: the
// cluster segment is the whole authorization decision, so "could not parse"
// must never leave a caller holding an empty cluster that then compares equal
// to an empty token claim.
func parseVirtualWorkspacePath(urlPath string) (virtualWorkspaceRequest, bool) {
	if !strings.HasPrefix(urlPath, apiurl.PathPrefixAPIExportVW+"/") {
		return virtualWorkspaceRequest{}, false
	}
	rest := strings.TrimPrefix(urlPath, apiurl.PathPrefixAPIExportVW+"/")

	// SplitN keeps the remainder intact — it is kcp's to route, and may be
	// absent entirely (discovery against the virtual workspace root).
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return virtualWorkspaceRequest{}, false
	}
	cluster, export := parts[0], parts[1]
	if !clusterSegmentRE.MatchString(cluster) || !exportSegmentRE.MatchString(export) {
		return virtualWorkspaceRequest{}, false
	}
	return virtualWorkspaceRequest{Cluster: cluster, Export: export}, true
}

const (
	vwRequiresProviderIdentityBody = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"APIExport virtual workspaces are addressable only by a provider's own ServiceAccount credential","reason":"Forbidden","code":403}`
	vwForeignExportBody            = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"this credential does not own an APIExport in that workspace","reason":"Forbidden","code":403}`
)

// writeForbidden writes a 403 with a Kubernetes Status body.
func writeForbidden(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, body)
}

// serveVirtualWorkspace relays an APIExport virtual-workspace request to kcp,
// path untouched, with the caller's own ServiceAccount token.
//
// Authorization is one structural comparison: the workspace the token was
// minted in must be the workspace holding the APIExport. A virtual workspace
// exists for its export's OWNER to observe every consumer, so the owner is the
// only identity with any business there — and a provider's ServiceAccount lives
// in the same workspace as the APIExport its `init` created. Nothing is looked
// up, so nothing can be raced or stale.
//
// This deliberately does not reuse the membership authorizer: that answers "may
// this user read this workspace", which is the wrong question. The path grants
// cross-cluster reach over every consumer of the export, and no consumer
// membership can imply that.
//
// kcp still applies its own RBAC to the relayed request, so this narrows rather
// than replaces it.
func (p *KCPProxy) serveVirtualWorkspace(w http.ResponseWriter, r *http.Request, token, tokenCluster string, vw virtualWorkspaceRequest) {
	// An SA token with no clusterName claim cannot be placed in any workspace,
	// so it can never own an export. Checked explicitly: without it an empty
	// claim would compare equal to an empty segment, and only the parser's
	// strictness would be standing between that and a bypass.
	if tokenCluster == "" {
		p.logger.Info("VW: SA token carries no clusterName claim", "path", r.URL.Path)
		writeForbidden(w, vwForeignExportBody)
		return
	}
	if tokenCluster != vw.Cluster {
		p.logger.Info("VW: cross-workspace access denied",
			"tokenCluster", tokenCluster, "requestedCluster", vw.Cluster, "export", vw.Export)
		writeForbidden(w, vwForeignExportBody)
		return
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// Path is forwarded verbatim. Everything after {export} belongs to
			// kcp's virtual workspace router, including the /clusters/* wildcard
			// that makes this a cross-cluster watch in the first place.
			req.Header.Set("Authorization", "Bearer "+token)
		},
		// FlushInterval -1 disables response buffering. Virtual-workspace
		// traffic is overwhelmingly long-lived watches; buffered, a watch event
		// would sit in the proxy until the next write and the provider would
		// reconcile late for no visible reason.
		FlushInterval: -1,
		Transport:     p.passthroughTransport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (virtual workspace)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"upstream error","reason":"ServiceUnavailable","code":502}`)
		},
	}

	p.logger.V(4).Info("VW: forwarding to kcp", "cluster", vw.Cluster, "export", vw.Export, "path", r.URL.Path)
	proxy.ServeHTTP(w, r)
}
