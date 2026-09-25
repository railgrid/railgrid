// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/client-go/dynamic"
)

// A verb is reached exactly one way: as a kcp CUSTOM SUBRESOURCE.
//
// The provider declares the verb on its APIExport as a resource entry named
// "<resource>/<verb>" whose storage is virtual, and kcp's shard reverse-proxies
// the request to the provider with the path intact, at
//
//	<endpoint>/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail...}][?component={component}]
//
// The caller is NOT a bearer: the shard authenticates the user itself, strips
// any inbound identity headers and stamps its own, exactly as the front proxy
// does when it reaches a shard. A provider therefore trusts these headers only
// over a connection it already trusts, and refuses the request outright when
// they are absent — anonymous is not a fallback, and the provider's own
// credential is never a substitute.
//
// serve's subresource adapter parses the path once, checks the coordinate
// against the provider's declaration, and hands the handler the parsed route
// (RouteFrom) and the caller (ProxiedIdentityFrom) in the request context.
// The URL itself reaches the handler unmodified.
const (
	// apisSegment introduces the group/version pair in the subresource form.
	apisSegment = "apis"

	// HeaderRemoteUser, HeaderRemoteGroup and HeaderRemoteExtraPrefix are the
	// requestheader names kcp stamps the caller's identity into. They match
	// kcp's defaults (pkg/proxy/authheaders).
	HeaderRemoteUser        = "X-Remote-User"
	HeaderRemoteGroup       = "X-Remote-Group"
	HeaderRemoteExtraPrefix = "X-Remote-Extra-"

	// ComponentQuery is the query parameter that carries the component of a
	// multi-component object on the kube path. The path itself cannot carry
	// it: kcp reads "{resource}/{name}/{subresource}" and everything after the
	// verb is the verb's own tail, so "components/{c}" in the path would be
	// routed as the subresource "components". The adapter turns the parameter
	// back into the component form a handler parses.
	ComponentQuery = "component"
	// HeaderHops counts how many proxies a request has crossed. kcp increments
	// it on every hop so a delegation cycle ends in a refusal rather than a
	// loop.
	HeaderHops = "X-Kcp-Internal-Proxy-Hops"

	// MaxHops is the ceiling this package enforces. kcp uses the same bound;
	// a request arriving above it has been round-tripping.
	MaxHops = 10
)

// SubresourceRequest is one parsed custom-subresource route. It carries
// everything Request does, plus the group and version the kube path names.
type SubresourceRequest struct {
	Request
	// Group and APIVersion come from the /apis/{group}/{version}/ segments.
	// The group is the provider's own, because a subresource is declared on
	// the export that owns the parent resource. (Request.Version, by
	// contrast, is an action's contract version.)
	Group      string
	APIVersion string
}

// ParseSubresourcePath parses the path a kcp shard forwards for a custom
// subresource. The cluster must be a cluster ID and never a workspace path,
// segments are bounded, and an empty or dotted segment is refused.
func ParseSubresourcePath(path string) (SubresourceRequest, error) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return SubresourceRequest{}, ErrBadPath
	}
	segments := strings.Split(trimmed, "/")
	// clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}[/tail...]
	if len(segments) < 8 || segments[0] != "clusters" || segments[2] != apisSegment {
		return SubresourceRequest{}, ErrBadPath
	}
	cluster := segments[1]
	if !IsClusterID(cluster) {
		return SubresourceRequest{}, ErrBadPath
	}
	group, version, resource, name, verb := segments[3], segments[4], segments[5], segments[6], segments[7]
	for _, segment := range []string{group, version, resource, name, verb} {
		if !validSegment(segment) {
			return SubresourceRequest{}, ErrBadPath
		}
	}
	// status and scale belong to the object's own shape and are served by
	// whatever serves the resource; they can never be a provider's verb.
	if verb == "status" || verb == "scale" {
		return SubresourceRequest{}, ErrBadPath
	}
	return SubresourceRequest{
		Request: Request{
			ClusterID: cluster,
			Resource:  resource,
			Name:      name,
			Verb:      verb,
			Tail:      strings.Join(segments[8:], "/"),
		},
		Group:      group,
		APIVersion: version,
	}, nil
}

// ParseSubresourceRequest is ParseSubresourcePath over a request. It refuses a
// path that arrived percent-encoded (URL.RawPath set), and reads the component
// of a multi-component object from the ComponentQuery parameter, applying the
// same segment rules the path does. A second value, or one with a separator
// in it, is ErrBadPath rather than a guess.
func ParseSubresourceRequest(r *http.Request) (SubresourceRequest, error) {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return SubresourceRequest{}, ErrBadPath
	}
	req, err := ParseSubresourcePath(r.URL.Path)
	if err != nil {
		return SubresourceRequest{}, err
	}
	if values, present := r.URL.Query()[ComponentQuery]; present {
		if len(values) != 1 || !validSegment(values[0]) {
			return SubresourceRequest{}, ErrBadPath
		}
		req.Component = values[0]
	}
	return req, nil
}

// ProxiedIdentity is the caller a kcp shard stamped onto a forwarded request.
//
// It is NOT interchangeable with Identity: there is no bearer here, so nothing
// downstream can act as the caller by presenting a token. What it can do is
// decide, and a provider must authorize with SubjectAccessReviews run on the
// caller's behalf rather than with a client that holds the caller's credential.
type ProxiedIdentity struct {
	// User is the authenticated user name the shard asserts.
	User string
	// Groups are that user's groups, in the order the shard sent them.
	Groups []string
	// Extra carries the requestheader extras, with the prefix stripped, the
	// key percent-decoded and lower-cased, which is how Kubernetes normalises
	// them.
	Extra map[string][]string
}

// ClusterNameExtra is the extra kcp attaches to a ServiceAccount identity
// naming the logical cluster the ServiceAccount lives in. Two providers'
// ServiceAccounts spell their names identically; this is what tells them apart.
const ClusterNameExtra = "authentication.kcp.io/cluster-name"

// ClusterName returns the logical cluster the identity's ServiceAccount lives
// in, or "" for any identity that is not a kcp ServiceAccount.
func (p ProxiedIdentity) ClusterName() string {
	for _, v := range p.Extra[ClusterNameExtra] {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// IsForeignProvider reports whether the identity is a ServiceAccount from a
// logical cluster other than the one the request addresses: another
// provider acting through its own APIExport virtual workspace, whose claim on
// this subresource kcp has already enforced before forwarding. A tenant's
// own ServiceAccount lives in the addressed cluster and is not foreign.
func (p ProxiedIdentity) IsForeignProvider(clusterID string) bool {
	if !strings.HasPrefix(p.User, "system:serviceaccount:") {
		return false
	}
	home := p.ClusterName()
	return home != "" && home != clusterID
}

// ErrNoProxiedIdentity is returned when the identity headers are missing. It is
// deliberately distinct from ErrNoBearer: the remedy is different, because this
// path is only reachable from a trusted front end.
var ErrNoProxiedIdentity = errors.New("dataplane: no proxied caller identity")

// ProxiedCaller reads the identity a shard stamped, and refuses a request that
// carries none.
//
// The headers are believed only because the connection is one the provider
// already trusts. Serving this path on an endpoint a tenant can reach directly
// would let anyone claim any identity, which is why the URL published in the
// endpoint object must be the provider's shard-facing address.
func ProxiedCaller(r *http.Request) (ProxiedIdentity, error) {
	if r == nil {
		return ProxiedIdentity{}, ErrNoProxiedIdentity
	}
	user := strings.TrimSpace(r.Header.Get(HeaderRemoteUser))
	if user == "" {
		return ProxiedIdentity{}, ErrNoProxiedIdentity
	}
	identity := ProxiedIdentity{User: user}
	for _, group := range r.Header.Values(HeaderRemoteGroup) {
		if group = strings.TrimSpace(group); group != "" {
			identity.Groups = append(identity.Groups, group)
		}
	}
	for name, values := range r.Header {
		if !strings.HasPrefix(http.CanonicalHeaderKey(name), HeaderRemoteExtraPrefix) {
			continue
		}
		key := strings.TrimPrefix(http.CanonicalHeaderKey(name), HeaderRemoteExtraPrefix)
		// kcp percent-encodes extra keys into the header name (a "/" in
		// authentication.kcp.io/cluster-name cannot travel in one); decode, as
		// the Kubernetes requestheader authenticator does, then lower-case.
		if decoded, err := url.PathUnescape(key); err == nil {
			key = decoded
		}
		key = strings.ToLower(key)
		if key == "" {
			continue
		}
		if identity.Extra == nil {
			identity.Extra = map[string][]string{}
		}
		identity.Extra[key] = append(identity.Extra[key], values...)
	}
	return identity, nil
}

// ErrTooManyHops is returned when a request has crossed more proxies than
// MaxHops. A cycle here costs one loop per request rather than a
// misconfiguration that only shows up under load, so it is refused early.
var ErrTooManyHops = errors.New("dataplane: too many proxy hops")

// CheckHops refuses a request that has been round-tripping between proxies.
// A missing or unparsable header counts as zero hops: kcp sets it, and a
// request that arrives without one came straight from something that does not.
func CheckHops(r *http.Request) error {
	if r == nil {
		return nil
	}
	raw := strings.TrimSpace(r.Header.Get(HeaderHops))
	if raw == "" {
		return nil
	}
	hops, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	if hops > MaxHops {
		return ErrTooManyHops
	}
	return nil
}

// The proxied identity travels in the request context from the subresource
// adapter to Gate. It is set by exactly one place — serve's subresource
// adapter, after it has parsed the shard's path and read the stamped headers —
// so a request that arrives on the hub-proxied grammar can never carry one,
// however it spells its headers.
type proxiedIdentityKey struct{}

// WithProxiedIdentity records the caller the shard stamped onto the request.
func WithProxiedIdentity(ctx context.Context, identity ProxiedIdentity) context.Context {
	return context.WithValue(ctx, proxiedIdentityKey{}, identity)
}

// ProxiedIdentityFrom reports the proxied caller, if this request came through
// the subresource adapter.
func ProxiedIdentityFrom(ctx context.Context) (ProxiedIdentity, bool) {
	identity, ok := ctx.Value(proxiedIdentityKey{}).(ProxiedIdentity)
	return identity, ok
}

// ProviderCallerFactory is a CallerFactory that can also act as the provider
// itself. The subresource path needs it: with no bearer there is no caller
// client to build, so visibility is decided by a SubjectAccessReview on the
// caller's behalf and the handler then acts as the provider. Nothing on the
// hub-proxied grammar ever calls AsProvider.
type ProviderCallerFactory interface {
	CallerFactory
	// AsProvider returns a client that acts as the provider in clusterID.
	AsProvider(clusterID string) (dynamic.Interface, error)
}

// The parsed route travels in the request context from serve's subresource
// adapter to the handler, beside the caller. The adapter is the one parser:
// it has already checked the coordinate against the provider's declaration
// and restored an action's contract version from it, neither of which a
// handler could do from the URL alone.
type routeKey struct{}

// WithRoute records the parsed route of a verb request.
func WithRoute(ctx context.Context, route SubresourceRequest) context.Context {
	return context.WithValue(ctx, routeKey{}, route)
}

// RouteFrom returns the route serve's adapter parsed for this request. A
// handler that finds none was not reached through the adapter and must refuse
// (ErrBadPath): nothing else is entitled to say what the request addresses.
func RouteFrom(ctx context.Context) (SubresourceRequest, bool) {
	route, ok := ctx.Value(routeKey{}).(SubresourceRequest)
	return route, ok
}
