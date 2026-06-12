// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/kuery/apis/query/v1alpha1"

	"github.com/faroshq/provider-kuery/queryapi"
)

// queryInput is the kuery_query tool input: a raw kuery QuerySpec. Kept as
// a JSON blob (not a typed mirror) so the tool tracks kuery's spec without
// a copy — the description carries the shape the model needs.
type queryInput struct {
	Spec json.RawMessage `json:"spec" jsonschema:"kuery QuerySpec JSON. Key fields: filter.objects[] (groupKind{group,kind}, namespace, name, labels, categories), cluster.name (an EDGE name to restrict to one edge; omit for the whole fleet), limit, objects.object (sparse projection, e.g. {metadata:{name:true},spec:{replicas:true}}), objects.relations{} (owners, owners+, descendants, descendants+, references, selects, selected-by, linked, linked+, grouped), maxDepth."`
}

type queryOutput struct {
	Status *v1alpha1.QueryStatus `json:"status"`
}

// impactInput identifies one object to expand the declared blast radius of.
type impactInput struct {
	Edge      string `json:"edge,omitempty" jsonschema:"edge (cluster) name the object lives on; omit if unique fleet-wide"`
	Group     string `json:"group,omitempty" jsonschema:"API group, empty for core"`
	Kind      string `json:"kind" jsonschema:"object kind, e.g. ConfigMap"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name" jsonschema:"object name"`
	MaxDepth  int32  `json:"maxDepth,omitempty" jsonschema:"transitive traversal depth (default 5, max 20)"`
}

type impactOutput struct {
	Status *v1alpha1.QueryStatus `json:"status"`
}

// impactRelations is the relation set the impact view expands: everything
// that DECLARES coupling to the object. Events excluded by default — they
// are blacklisted from sync.
var impactRelations = []string{"descendants+", "references", "selects", "selected-by", "owners", "linked+", "grouped"}

func registerTools(srv *mcp.Server, deps Deps, r *http.Request) {
	ident := queryapi.IdentityFromRequest(r)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kuery_query",
		Title:       "Query objects across the edge fleet",
		Description: "Run one kuery query over every edge cluster connected to this workspace: filter by kind/namespace/labels, project sparse fields, and expand relations. Use instead of per-edge kubectl when the question spans edges.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, queryOutput, error) {
		if ident.Tenant == "" {
			return nil, queryOutput{}, fmt.Errorf("missing tenant identity")
		}
		var spec v1alpha1.QuerySpec
		if len(in.Spec) > 0 {
			if err := json.Unmarshal(in.Spec, &spec); err != nil {
				return nil, queryOutput{}, fmt.Errorf("invalid QuerySpec: %w", err)
			}
		}
		queryapi.ScopeToTenant(&spec, ident.Tenant)
		status, err := deps.Engine.Execute(ctx, &spec)
		if err != nil {
			return nil, queryOutput{}, fmt.Errorf("query failed: %w", err)
		}
		return nil, queryOutput{Status: status}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kuery_impact",
		Title:       "Blast radius of one object",
		Description: "Expand the declared coupling of one object across the fleet: transitive descendants, spec references (e.g. pods mounting a ConfigMap), label-selector matches both ways, owners, and cross-edge links. Reliable for 'who consumes this — safe to change?' questions; does not see network-level dependencies.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in impactInput) (*mcp.CallToolResult, impactOutput, error) {
		if ident.Tenant == "" {
			return nil, impactOutput{}, fmt.Errorf("missing tenant identity")
		}
		if in.Kind == "" || in.Name == "" {
			return nil, impactOutput{}, fmt.Errorf("kind and name are required")
		}
		maxDepth := in.MaxDepth
		if maxDepth == 0 {
			maxDepth = 5
		}

		relations := map[string]v1alpha1.RelationSpec{}
		for _, rel := range impactRelations {
			relations[rel] = v1alpha1.RelationSpec{}
		}
		spec := v1alpha1.QuerySpec{
			Cluster:  &v1alpha1.ClusterFilter{Name: in.Edge},
			MaxDepth: maxDepth,
			Filter: &v1alpha1.QueryFilter{
				Objects: []v1alpha1.ObjectFilter{{
					GroupKind: &v1alpha1.GroupKindFilter{APIGroup: in.Group, Kind: in.Kind},
					Namespace: in.Namespace,
					Name:      in.Name,
				}},
			},
			Objects: &v1alpha1.ObjectsSpec{
				ID:        true,
				Cluster:   true,
				Relations: relations,
			},
		}
		if in.Edge == "" {
			spec.Cluster = nil
		}
		queryapi.ScopeToTenant(&spec, ident.Tenant)
		status, err := deps.Engine.Execute(ctx, &spec)
		if err != nil {
			return nil, impactOutput{}, fmt.Errorf("impact query failed: %w", err)
		}
		return nil, impactOutput{Status: status}, nil
	})
}
