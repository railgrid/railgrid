// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

const providerDiscoveryLimit = 1000

// providerResourceDiscovery exposes metadata, never arbitrary resource bodies.
// The signed initiating caller must be allowed to list the advertised kind in
// this exact tenant. No list permission is minted for the consuming provider.
type providerResourceDiscovery struct {
	verify   func(context.Context, *http.Request) (actionproof.Claims, error)
	registry *providers.Registry
	bound    func(context.Context, string, providers.Provider) (bool, error)
	// client impersonates the identity authenticated and signed by the hub.
	// Native kcp RBAC and APIBinding resolution still govern the actual read.
	client func(actionproof.Claims) (dynamic.Interface, error)
}

type discoveredProviderResource struct {
	Metadata discoveredProviderResourceMetadata `json:"metadata"`
}

type discoveredProviderResourceMetadata struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

func (h *providerResourceDiscovery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeDiscoveryError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only resource discovery reads are supported.")
		return
	}
	if h.verify == nil || h.registry == nil || h.client == nil || h.bound == nil {
		writeDiscoveryError(w, http.StatusServiceUnavailable, "discovery_unavailable", "Provider resource discovery is unavailable.")
		return
	}
	caller, err := h.verify(r.Context(), r)
	if err != nil {
		writeDiscoveryError(w, http.StatusUnauthorized, "caller_context_required", "A current verified caller context is required for provider discovery.")
		return
	}
	vars := mux.Vars(r)
	name, resource := vars["provider"], vars["resource"]
	apiVersion := r.URL.Query().Get("apiVersion")
	entry, ok := h.registry.GetForOrg(caller.OrgUUID, name)
	if !ok || entry.Export == nil {
		writeDiscoveryError(w, http.StatusNotFound, "provider_unavailable", "The requested provider resource is unavailable.")
		return
	}
	ready, _, _ := entry.Readiness()
	if !ready {
		writeDiscoveryError(w, http.StatusServiceUnavailable, "provider_unavailable", "The requested provider is not ready.")
		return
	}
	// Only a declared, current action-bearing resource is discoverable. This
	// endpoint cannot be used as a general Kubernetes proxy or Secret reader.
	kind := ""
	for _, exported := range entry.Export.Resources {
		if exported.Name != resource || exported.APIVersion != apiVersion {
			continue
		}
		for _, action := range exported.Actions {
			if action.Deprecation == nil || !action.Deprecation.Deprecated {
				kind = exported.Kind
				break
			}
		}
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil || gv.Group == "" || gv.Version == "" || kind == "" {
		writeDiscoveryError(w, http.StatusNotFound, "resource_not_discoverable", "The requested resource has no current advertised actions.")
		return
	}
	bound, err := h.bound(r.Context(), caller.ClusterID, entry)
	if err != nil {
		writeDiscoveryError(w, http.StatusServiceUnavailable, "binding_unavailable", "Provider enablement could not be verified.")
		return
	}
	if !bound {
		writeDiscoveryError(w, http.StatusForbidden, "provider_not_enabled", "The requested provider is not enabled in this workspace.")
		return
	}
	client, err := h.client(caller)
	if err != nil {
		writeDiscoveryError(w, http.StatusServiceUnavailable, "discovery_unavailable", "Provider resource discovery is unavailable.")
		return
	}
	list, err := client.Resource(gv.WithResource(resource)).List(r.Context(), metav1.ListOptions{Limit: providerDiscoveryLimit})
	if err != nil {
		status := http.StatusServiceUnavailable
		code, message := "discovery_unavailable", "Provider resources could not be discovered. Retry when the provider is available."
		if reason := apierrors.ReasonForError(err); reason == metav1.StatusReasonForbidden || reason == metav1.StatusReasonUnauthorized {
			status, code, message = http.StatusForbidden, "resource_access_denied", "The caller is not allowed to discover this provider resource."
		}
		writeDiscoveryError(w, status, code, message)
		return
	}
	items := make([]discoveredProviderResource, 0, len(list.Items))
	for _, object := range list.Items {
		if len(items) == providerDiscoveryLimit {
			break
		}
		if object.GetDeletionTimestamp() != nil {
			continue
		}
		items = append(items, discoveredProviderResource{Metadata: discoveredProviderResourceMetadata{
			Name: object.GetName(), UID: string(object.GetUID()), ResourceVersion: object.GetResourceVersion(),
		}})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		APIVersion string                       `json:"apiVersion"`
		Kind       string                       `json:"kind"`
		Resource   string                       `json:"resource"`
		Items      []discoveredProviderResource `json:"items"`
		Truncated  bool                         `json:"truncated"`
	}{apiVersion, kind, resource, items, list.GetContinue() != "" || len(list.Items) > providerDiscoveryLimit})
}

func writeDiscoveryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
