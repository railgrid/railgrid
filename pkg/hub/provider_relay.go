// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func providerRelayAuthorization(config *rest.Config) providers.RelayAuthorization {
	return func(ctx context.Context, p providers.Provider, route dataplane.SubresourceRequest) error {
		declared := false
		for _, c := range p.Export.Coordinates() {
			gv := c.GroupVersion()
			if c.Resource == route.Resource && c.Verb == route.Verb && gv.Group == route.Group && gv.Version == route.APIVersion {
				declared = true
				break
			}
		}
		if !declared {
			return fmt.Errorf("undeclared provider coordinate")
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, route.ClusterID)
		cfg.Timeout = 10 * time.Second
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return err
		}
		lc, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}
		prefix := kcppaths.OrgPath(p.OrgUUID) + ":"
		path := lc.GetAnnotations()["kcp.io/path"]
		if !strings.HasPrefix(path, prefix) || strings.Contains(strings.TrimPrefix(path, prefix), ":") || strings.TrimPrefix(path, prefix) == "providers" {
			return fmt.Errorf("provider consumer is outside owning organization")
		}
		bindings, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, binding := range bindings.Items {
			phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
			name, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
			exportPath, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
			if phase != "Bound" || name != p.APIExportName || (exportPath != p.APIExportPath && exportPath != p.CatalogEntryCluster) {
				continue
			}
			resources, _, _ := unstructured.NestedSlice(binding.Object, "status", "boundResources")
			for _, raw := range resources {
				resource, ok := raw.(map[string]any)
				if ok && resource["group"] == route.Group && (resource["resource"] == route.Resource || resource["resource"] == route.Resource+"/"+route.Verb) {
					return nil
				}
			}
		}
		return fmt.Errorf("no matching provider APIBinding")
	}
}

// kcp asks the referenced virtual storage for discovery before advertising a
// custom subresource. Read the hub-controlled export and request schemas; no
// provider-authored URL is fetched to answer this shard-only request.
func providerRelayDiscovery(config *rest.Config) func(context.Context, providers.Provider, string, string) (*metav1.APIResourceList, error) {
	return func(ctx context.Context, p providers.Provider, group, version string) (*metav1.APIResourceList, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, p.CatalogEntryCluster)
		cfg.Timeout = 10 * time.Second
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		export, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}).Get(ctx, p.APIExportName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		resources, _, err := unstructured.NestedSlice(export.Object, "spec", "resources")
		if err != nil || len(resources) > 512 {
			return nil, fmt.Errorf("invalid provider export")
		}
		list := &metav1.APIResourceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"}, GroupVersion: group + "/" + version}
		for _, raw := range resources {
			resource, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := resource["name"].(string)
			resourceGroup, _ := resource["group"].(string)
			schemaName, _ := resource["schema"].(string)
			if resourceGroup != group || !strings.Contains(name, "/") || schemaName == "" {
				continue
			}
			declared := false
			for _, c := range p.Export.Coordinates() {
				if c.String() == name && c.APIVersion == group+"/"+version {
					declared = true
					break
				}
			}
			if !declared {
				continue
			}
			shape, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}).Get(ctx, schemaName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			kind, _, _ := unstructured.NestedString(shape.Object, "spec", "names", "kind")
			scope, _, _ := unstructured.NestedString(shape.Object, "spec", "scope")
			versions, _, _ := unstructured.NestedSlice(shape.Object, "spec", "versions")
			served := false
			for _, rawVersion := range versions {
				v, ok := rawVersion.(map[string]any)
				if ok && v["name"] == version && v["served"] == true {
					served = true
				}
			}
			if kind == "" || !served {
				continue
			}
			list.APIResources = append(list.APIResources, metav1.APIResource{Name: name, Namespaced: scope == "Namespaced", Kind: kind, Verbs: metav1.Verbs{"get", "create", "update", "patch", "delete"}})
		}
		return list, nil
	}
}

// ProviderRelayBaseURL is also used by the hub-mediated org bootstrap handler.
// Embedded dispatch never resolves its reserved internal hostname on a network.
func (s *Server) ProviderRelayBaseURL() string {
	if s.opts.EmbeddedKCP {
		return providers.EmbeddedProviderRelayURL
	}
	return s.opts.ProviderRelayURL
}

func (s *Server) startProviderRelay(ctx context.Context, handler http.Handler) (<-chan error, error) {
	if s.opts.EmbeddedKCP {
		return nil, nil
	}
	if s.opts.ProviderRelayURL == "" && s.opts.ProviderRelayListenAddr == "" {
		return nil, nil
	}
	u, err := url.Parse(s.opts.ProviderRelayURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || s.opts.ProviderRelayListenAddr == "" {
		return nil, fmt.Errorf("external provider relay requires an HTTPS origin and dedicated listen address")
	}
	config, err := providers.ShardRelayTLSConfig(s.opts.ProviderRelayClientCAFile, s.opts.ProviderRelayAllowedNames)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(s.opts.ProviderRelayCertFile, s.opts.ProviderRelayKeyFile)
	if err != nil {
		return nil, fmt.Errorf("provider relay serving certificate: %w", err)
	}
	config.Certificates = []tls.Certificate{cert}
	listener, err := net.Listen("tcp", s.opts.ProviderRelayListenAddr)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: providers.AuthenticatedShardHandler(handler), TLSConfig: config, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 * 1024}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(tls.NewListener(listener, config))
		if err != http.ErrServerClosed {
			done <- err
		}
		close(done)
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	return done, nil
}
