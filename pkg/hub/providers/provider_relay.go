// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/providerrelay"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const ProviderRelayPrefix = "/provider-dataplane/"
const EmbeddedProviderRelayURL = "http://railgrid-provider-relay.internal"

// ProviderDataPlaneURL is the only endpoint URL the hub publishes for an org
// provider. kcp appends the original /clusters/{consumer}/apis/... request.
func ProviderDataPlaneURL(base, catalogCluster, provider string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") || !dataplane.IsClusterID(catalogCluster) || len(validation.IsDNS1123Label(provider)) != 0 {
		return "", fmt.Errorf("invalid provider relay endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/") + ProviderRelayPrefix + catalogCluster + "/" + provider
	u.RawPath = ""
	return u.String(), nil
}

type RelayAuthorization func(context.Context, Provider, dataplane.SubresourceRequest) error
type trustedShardRelay struct{}

// ProviderRelay accepts requests only from the dedicated authenticated shard
// listener or the embedded shard's in-process transport.
type ProviderRelay struct {
	Registry  *Registry
	Proxy     *ProviderProxy
	Keys      serviceaccounts.ProofKeySource
	Authorize RelayAuthorization
	Discovery func(context.Context, Provider, string, string) (*metav1.APIResourceList, error)
}

func (h *ProviderRelay) PublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, err := h.signingKey(r.Context())
	if err != nil {
		http.Error(w, "relay key unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))))
}

func (h *ProviderRelay) signingKey(ctx context.Context) (ed25519.PrivateKey, error) {
	if h.Keys == nil {
		return nil, fmt.Errorf("relay keys unavailable")
	}
	root, err := h.Keys.DelegatedProofKey(ctx)
	if err != nil {
		return nil, err
	}
	return providerrelay.SigningKey(root)
}

func (h *ProviderRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Context().Value(trustedShardRelay{}) != true {
		http.Error(w, "shard authentication required", http.StatusUnauthorized)
		return
	}
	cluster, name, kubePath, ok := parseProviderRelayPath(r)
	if !ok {
		http.Error(w, "invalid provider route", http.StatusBadRequest)
		return
	}
	var prov Provider
	for _, candidate := range h.Registry.List() {
		if candidate.CatalogEntryCluster == cluster && candidate.Name == name {
			prov = candidate
			break
		}
	}
	if prov.OrgUUID == "" || !prov.Ready() {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.HasPrefix(kubePath, "/apis/") {
		parts := strings.Split(strings.TrimPrefix(kubePath, "/apis/"), "/")
		if r.Method != http.MethodGet || len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(kubePath, "%\\") || h.Discovery == nil {
			http.NotFound(w, r)
			return
		}
		list, err := h.Discovery(r.Context(), prov, parts[0], parts[1])
		if err != nil {
			http.Error(w, "provider discovery unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	forward := r.Clone(r.Context())
	u := *r.URL
	u.Path, u.RawPath = kubePath, ""
	forward.URL = &u
	route, err := dataplane.ParseSubresourceRequest(forward)
	if err != nil || dataplane.CheckHops(r) != nil {
		http.Error(w, "invalid provider request", http.StatusBadRequest)
		return
	}
	identity, err := dataplane.ProxiedCaller(r)
	if err != nil {
		http.Error(w, "caller identity required", http.StatusUnauthorized)
		return
	}
	if h.Authorize == nil || h.Authorize(r.Context(), prov, route) != nil {
		http.Error(w, "provider binding denied", http.StatusForbidden)
		return
	}
	hop, err := h.Proxy.resolveEdgeHop(prov)
	if err != nil {
		http.Error(w, "provider tunnel unavailable", http.StatusServiceUnavailable)
		return
	}
	key, err := h.signingKey(r.Context())
	if err != nil {
		http.Error(w, "relay key unavailable", http.StatusServiceUnavailable)
		return
	}
	hops, _ := strconv.Atoi(r.Header.Get(dataplane.HeaderHops))
	if hops < 1 {
		hops = 1
	}
	proof, err := providerrelay.Sign(key, providerrelay.Claims{Cluster: hop.route.Cluster, Service: hop.route.ServiceName, Method: r.Method, Path: kubePath, Query: r.URL.RawQuery, Identity: identity, Hops: hops + 1}, time.Now())
	if err != nil {
		http.Error(w, "invalid caller context", http.StatusBadRequest)
		return
	}
	dst := hop.url(kubePath)
	rp := &httputil.ReverseProxy{Transport: hop.transport, FlushInterval: -1, ModifyResponse: func(resp *http.Response) error {
		return orgProviderModifyResponse(resp, r.Header)
	}, Director: func(out *http.Request) {
		out.URL.Scheme, out.URL.Host, out.URL.Path, out.URL.RawPath = dst.Scheme, dst.Host, dst.Path, ""
		out.Host = dst.Host
		stripShardIdentityHeaders(out.Header)
		out.Header.Del("Authorization")
		out.Header.Del(dataplane.HeaderUpstreamAuthorization)
		out.Header.Del("Cookie")
		out.Header.Set(providerrelay.Header, proof)
	}}
	rp.ServeHTTP(newOrgProviderResponseWriter(w), r)
}

func parseProviderRelayPath(r *http.Request) (string, string, string, bool) {
	if r.URL.RawPath != "" || !strings.HasPrefix(r.URL.Path, ProviderRelayPrefix) {
		return "", "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, ProviderRelayPrefix), "/", 3)
	if len(parts) != 3 || !dataplane.IsClusterID(parts[0]) || len(validation.IsDNS1123Label(parts[1])) != 0 {
		return "", "", "", false
	}
	return parts[0], parts[1], "/" + parts[2], true
}

func trustedProviderRelay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), trustedShardRelay{}, true)))
	})
}
