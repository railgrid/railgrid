// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"net/http"
)

// QuerySpecSchema is a JSON Schema (draft-07) for the kuery QuerySpec — the
// body POSTed to /api/query. It is intentionally a curated, hand-authored
// subset of the full kuery type (the fields a human or an editor actually
// needs), not a generated dump: it powers the playground's editor
// autocomplete/validation AND doubles as external API documentation. Keep the
// relation enum in lockstep with the engine's relation set.
const QuerySpecSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "kuery QuerySpec",
  "description": "A single query across every edge cluster engaged for your workspace. POST to /api/query; the response is QueryStatus.objects[].",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "root": {
      "type": "string",
      "enum": ["objects", "clusters"],
      "description": "What to root on. 'objects' (default) returns Kubernetes objects. 'clusters' returns one node per engaged edge, whose 'members' relation expands to its objects (the per-cluster tree)."
    },
    "cluster": {
      "type": "object",
      "description": "Restrict to one edge. The hub scopes every query to your tenant regardless; this narrows further to a single edge.",
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string", "description": "Edge name, e.g. dev-edge-kube-1." }
      }
    },
    "limit": { "type": "integer", "description": "Max root objects (default 100, max 10000)." },
    "maxDepth": { "type": "integer", "description": "Transitive relation depth for '+' relations (default 10, max 20)." },
    "count": { "type": "boolean", "description": "Also return the total count of matching root objects (expensive)." },
    "filter": {
      "type": "object",
      "description": "Object-level filters. filter.objects[] entries are OR-ed; criteria within one entry are AND-ed.",
      "additionalProperties": false,
      "properties": {
        "objects": {
          "type": "array",
          "items": { "$ref": "#/definitions/objectFilter" }
        }
      }
    },
    "objects": { "$ref": "#/definitions/objectsSpec" }
  },
  "definitions": {
    "objectFilter": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "groupKind": {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "apiGroup": { "type": "string", "description": "API group, empty string for core." },
            "kind": { "type": "string", "description": "Kind, resource, singular, or short name (e.g. Deployment, deployments, deploy)." }
          }
        },
        "name": { "type": "string", "description": "Exact object name." },
        "namespace": { "type": "string", "description": "Namespace." },
        "labels": { "type": "object", "description": "matchLabels-style key=value (AND).", "additionalProperties": { "type": "string" } },
        "categories": { "type": "array", "items": { "type": "string" }, "description": "Resource categories, e.g. all." },
        "id": { "type": "string", "description": "Opaque object id from a previous result." },
        "jsonpath": { "type": "string", "description": "Boolean JSONPath filter (last resort), e.g. $.status.phase." }
      }
    },
    "objectsSpec": {
      "type": "object",
      "description": "Response shape: which fields to return and which relations to expand.",
      "additionalProperties": false,
      "properties": {
        "id": { "type": "boolean", "description": "Include the opaque object id." },
        "cluster": { "type": "boolean", "description": "Include the edge/cluster name." },
        "mutablePath": { "type": "boolean", "description": "Include the REST path for direct mutation." },
        "object": { "type": "object", "description": "Sparse projection: { metadata: { name: true }, spec: { replicas: true } }. Omit for the full object." },
        "relations": { "$ref": "#/definitions/relations" }
      }
    },
    "relations": {
      "type": "object",
      "description": "Nested related objects, keyed by relation. Use '+' for the transitive form (descendants+, owners+, linked+).",
      "additionalProperties": { "$ref": "#/definitions/relationSpec" },
      "properties": {
        "owners":       { "$ref": "#/definitions/relationSpec", "description": "UPSTREAM: objects that own this (deleting them impacts this)." },
        "descendants":  { "$ref": "#/definitions/relationSpec", "description": "DOWNSTREAM: objects this owns (impacted if this is deleted)." },
        "references":   { "$ref": "#/definitions/relationSpec", "description": "UPSTREAM: spec field refs, e.g. a Pod's ConfigMaps/Secrets/ServiceAccount." },
        "selects":      { "$ref": "#/definitions/relationSpec", "description": "UPSTREAM: objects this selects via spec.selector." },
        "selected-by":  { "$ref": "#/definitions/relationSpec", "description": "DOWNSTREAM: selectors that match this (e.g. Services)." },
        "namespace":    { "$ref": "#/definitions/relationSpec", "description": "UPSTREAM: the Namespace this lives in." },
        "namespaced":   { "$ref": "#/definitions/relationSpec", "description": "DOWNSTREAM: every object in this Namespace." },
        "members":      { "$ref": "#/definitions/relationSpec", "description": "DOWNSTREAM: every object in this cluster (use with root=clusters)." },
        "linked":       { "$ref": "#/definitions/relationSpec", "description": "LATERAL: kuery.io/relates-to annotation links (cross-edge)." },
        "grouped":      { "$ref": "#/definitions/relationSpec", "description": "LATERAL: shared kuery.io/group label (cross-edge)." }
      }
    },
    "relationSpec": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "limit": { "type": "integer", "description": "Max related objects per parent." },
        "filters": { "type": "array", "items": { "$ref": "#/definitions/objectFilter" }, "description": "Restrict which related objects are returned." },
        "objects": { "$ref": "#/definitions/objectsSpec" }
      }
    }
  }
}`

// SchemaHandler serves the QuerySpec JSON Schema. It needs no tenant identity —
// the schema is the same for everyone — so it's safe to call unauthenticated,
// which lets external clients fetch it for codegen/docs.
type SchemaHandler struct{}

func (SchemaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/schema+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(QuerySpecSchema))
}
