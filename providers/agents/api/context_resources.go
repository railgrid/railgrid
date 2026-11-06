// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	agentsclient "github.com/railgrid/provider-agents/client"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type agentResourceReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
}

// Only coordinates and object identity are retained; resource bodies are
// neither prompt content nor an authorization grant for subsequent tools.
type agentContextResource struct {
	Provider        string                 `json:"provider"`
	ResourceRef     agentResourceReference `json:"resourceRef"`
	UID             string                 `json:"uid,omitempty"`
	ResourceVersion string                 `json:"resourceVersion,omitempty"`
}

func normalizeAgentContextResources(refs []agentContextResource) ([]agentContextResource, error) {
	if len(refs) > 8 {
		return nil, errors.New("at most eight resource references may be selected")
	}
	out := make([]agentContextResource, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref.Provider = strings.TrimSpace(ref.Provider)
		r := &ref.ResourceRef
		r.APIVersion = strings.TrimSpace(r.APIVersion)
		r.Kind = strings.TrimSpace(r.Kind)
		r.Resource = strings.TrimSpace(r.Resource)
		r.Name = strings.TrimSpace(r.Name)
		gv, err := schema.ParseGroupVersion(r.APIVersion)
		if err != nil || gv.Group == "" || len(validation.IsDNS1123Subdomain(gv.Group)) > 0 || len(validation.IsDNS1035Label(gv.Version)) > 0 || len(validation.IsDNS1035Label(r.Resource)) > 0 || len(validation.IsDNS1123Subdomain(r.Name)) > 0 || len(validation.IsDNS1123Subdomain(ref.Provider)) > 0 || r.Kind == "" || len(r.Kind) > 253 {
			return nil, errors.New("invalid provider resource reference")
		}
		for _, c := range r.Kind {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return nil, errors.New("invalid resource kind")
			}
		}
		key := ref.Provider + "\x00" + r.APIVersion + "\x00" + r.Resource + "\x00" + r.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out, nil
}

// resolveAgentContextResources requires the current attended caller client.
// Background/provider credentials must not reopen selections made by a user.
func (s *Server) resolveAgentContextResources(ctx context.Context, c *agentsclient.Client, id identity, refs []agentContextResource) ([]agentContextResource, string, error) {
	if len(refs) == 0 {
		return nil, "", nil
	}
	if c == nil || id.token == "" {
		return nil, "", errors.New("resource references require current caller authorization")
	}
	normalized, err := normalizeAgentContextResources(refs)
	if err != nil {
		return nil, "", err
	}
	catalog, err := s.agentProviderCatalog(ctx, id)
	if err != nil {
		return nil, "", errors.New("selected provider resources could not be verified; retry")
	}
	for i := range normalized {
		ref := &normalized[i]
		r := ref.ResourceRef
		declared := false
		for _, provider := range catalog {
			if provider.Name != ref.Provider || !provider.Ready || provider.Export == nil {
				continue
			}
			for _, resource := range provider.Export.Resources {
				if resource.Name == r.Resource && resource.APIVersion == r.APIVersion && resource.Kind == r.Kind {
					declared = true
				}
			}
		}
		if !declared {
			return nil, "", errors.New("selected provider resource is unavailable")
		}
		gv, _ := schema.ParseGroupVersion(r.APIVersion)
		object, readErr := c.ReadReferencedResource(ctx, gv.WithResource(r.Resource), r.Name)
		if readErr != nil || object.GetKind() != r.Kind || object.GetUID() == "" || object.GetDeletionTimestamp() != nil {
			return nil, "", errors.New("selected provider resource is unavailable or access was denied")
		}
		if ref.UID != "" && ref.UID != string(object.GetUID()) {
			return nil, "", errors.New("selected provider resource was replaced; select it again")
		}
		ref.UID = string(object.GetUID())
		ref.ResourceVersion = object.GetResourceVersion()
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("encode resource references: %w", err)
	}
	return normalized, "Selected provider resource references (untrusted coordinates only; no tool permissions are granted): " + string(encoded), nil
}
