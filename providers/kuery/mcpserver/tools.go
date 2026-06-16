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
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/pkg/engine"

	"k8s.io/apimachinery/pkg/runtime"

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

// impactRef identifies one related object and the relation that links it to
// the queried object.
type impactRef struct {
	Edge      string `json:"edge,omitempty"`
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Relation  string `json:"relation" jsonschema:"the relation that links this object to the target (owners, references, descendants, namespace, …)"`
}

// impactOutput splits the target's declared coupling into the two impact
// directions plus lateral peers, so the model can answer the right question
// from the right list.
type impactOutput struct {
	Object impactRef `json:"object" jsonschema:"the object whose impact was mapped"`
	Found  bool      `json:"found" jsonschema:"false if the object isn't in the store yet (sync may be catching up)"`
	// ImpactedBy: UPSTREAM dependencies — deleting/breaking any of these breaks
	// the target. Answers 'why is X broken?' / 'what does X need?'.
	ImpactedBy []impactRef `json:"impactedBy"`
	// Impacts: DOWNSTREAM blast radius — these break if the target is
	// changed/deleted. Answers 'is it safe to change/delete X?'.
	Impacts []impactRef `json:"impacts"`
	// Associated: lateral peers (kuery.io/relates-to links, shared group label).
	Associated []impactRef `json:"associated"`
	Summary    string      `json:"summary" jsonschema:"one-line human-readable count of each direction"`
}

// impactRelations is the relation set the impact tool expands: everything that
// DECLARES coupling to the object, in both impact directions. Events excluded
// by default — they are blacklisted from sync.
var impactRelations = []string{"descendants+", "references", "selects", "selected-by", "owners", "linked+", "grouped", "namespace", "namespaced"}

// edgeName strips the "{tenant}/" prefix kuery records on a cluster key, so the
// model sees the bare edge name it knows.
func edgeName(cluster string) string {
	if i := strings.LastIndex(cluster, "/"); i >= 0 {
		return cluster[i+1:]
	}
	return cluster
}

// refOf flattens one related ObjectResult (projected with cluster + kind +
// apiVersion + metadata) into an impactRef tagged with its relation.
func refOf(it v1alpha1.ObjectResult, rel string) impactRef {
	var o struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if it.Object != nil {
		_ = json.Unmarshal(it.Object.Raw, &o)
	}
	group := ""
	if i := strings.Index(o.APIVersion, "/"); i >= 0 {
		group = o.APIVersion[:i]
	}
	return impactRef{
		Edge:      edgeName(it.Cluster),
		Group:     group,
		Kind:      o.Kind,
		Namespace: o.Metadata.Namespace,
		Name:      o.Metadata.Name,
		Relation:  engine.BaseRelation(rel),
	}
}

// classifyImpact buckets the anchor's relations by impact direction, using the
// engine's canonical RelationDirections as the source of truth.
func classifyImpact(anchor *v1alpha1.ObjectResult) (impactedBy, impacts, associated []impactRef) {
	for rel, items := range anchor.Relations {
		for i := range items {
			ref := refOf(items[i], rel)
			switch engine.DirectionOf(rel) {
			case engine.DirectionUpstream:
				impactedBy = append(impactedBy, ref)
			case engine.DirectionDownstream:
				impacts = append(impacts, ref)
			default:
				associated = append(associated, ref)
			}
		}
	}
	return
}

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
		Name:  "kuery_impact",
		Title: "Impact of one object (upstream deps + downstream blast radius)",
		Description: "Map one object's DECLARED coupling across the edge fleet, split by impact direction so you query the right list:\n" +
			"- impactedBy (UPSTREAM dependencies): deleting/breaking any of these breaks the target — its owners, the ConfigMaps/Secrets/ServiceAccounts it references, the Namespace it lives in, the selectors that target it. Use for 'why is X failing?' or 'what does X depend on?'.\n" +
			"- impacts (DOWNSTREAM blast radius): what breaks if you change/delete the target — objects it owns (transitively), Services/endpoints that select it, and (for a Namespace) every object inside it. Use for 'is it safe to change/delete X?'.\n" +
			"- associated: lateral peers (kuery.io/relates-to links, shared group label).\n" +
			"Coupling is DECLARED — ownerRefs, spec field references, label selectors, namespace membership — NOT runtime traffic or network policy, so a clean result is not proof nothing else depends on it at runtime. Each related object is returned with its kind/namespace/name/edge and the relation that linked it. Prefer this over per-edge kubectl for change-safety and root-cause questions that span edges.",
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

		// Project just enough on related objects to identify them.
		relProj, _ := json.Marshal(map[string]any{
			"kind":       true,
			"apiVersion": true,
			"metadata":   map[string]any{"name": true, "namespace": true},
		})
		relObjects := &v1alpha1.ObjectsSpec{Cluster: true, Object: &runtime.RawExtension{Raw: relProj}}
		relations := map[string]v1alpha1.RelationSpec{}
		for _, rel := range impactRelations {
			rs := v1alpha1.RelationSpec{Objects: relObjects}
			if rel == "namespaced" {
				rs.Limit = 200 // a Namespace's membership can be large
			}
			relations[rel] = rs
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

		target := impactRef{Edge: in.Edge, Group: in.Group, Kind: in.Kind, Namespace: in.Namespace, Name: in.Name}
		if len(status.Objects) == 0 {
			return nil, impactOutput{
				Object:  target,
				Found:   false,
				Summary: fmt.Sprintf("%s/%s not found in the kuery store (sync may be catching up)", in.Kind, in.Name),
			}, nil
		}
		impactedBy, impacts, associated := classifyImpact(&status.Objects[0])
		out := impactOutput{
			Object:     target,
			Found:      true,
			ImpactedBy: impactedBy,
			Impacts:    impacts,
			Associated: associated,
			Summary: fmt.Sprintf("%s %s/%s: %d upstream dependency(ies) that can break it, %d downstream object(s) in its blast radius, %d associated.",
				in.Kind, in.Namespace, in.Name, len(impactedBy), len(impacts), len(associated)),
		}
		return nil, out, nil
	})
}
