// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

// QuerySpecSchema is a JSON Schema (draft-07) for the kuery QuerySpec — a
// SavedView's spec.query, and the optional override the run verb accepts. It
// is intentionally a curated, hand-authored subset of the full kuery type (the
// fields a human or an editor actually needs), not a generated dump: it powers
// the playground's editor autocomplete, it is what ValidateQuerySpec checks a
// saved view against, and it doubles as external API documentation. Keep the
// relation enum in lockstep with the engine's relation set.
const QuerySpecSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "kuery QuerySpec",
  "description": "A single query across every edge cluster engaged for your workspace. It is a SavedView's spec.query; run it with POST /dataplane/clusters/{clusterID}/savedviews/{name}/run and the result is QueryStatus.objects[].",
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
    "page": {
      "type": "object",
      "description": "Pagination parameters. Use first for an offset or cursor for an opaque keyset cursor returned by a previous response.",
      "additionalProperties": false,
      "properties": {
        "first": { "type": "integer", "minimum": 0, "description": "Number of root objects to skip (offset pagination)." },
        "cursor": { "type": "string", "description": "Opaque cursor from QueryStatus.cursor.next; pass it back unchanged." }
      }
    },
    "order": {
      "type": "array",
      "description": "Stable root-object sort order. Kuery appends namespace and name ascending tie-breakers when absent.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["field"],
        "properties": {
          "field": { "type": "string", "enum": ["name", "namespace", "kind", "apiGroup", "cluster", "creationTimestamp"] },
          "direction": { "type": "string", "enum": ["Asc", "Desc"] }
        }
      }
    },
    "maxDepth": { "type": "integer", "description": "Transitive relation depth for '+' relations (default 10, max 20)." },
    "count": { "type": "boolean", "description": "Also return the total count of matching root objects (expensive)." },
    "cursor": { "type": "boolean", "description": "Include an opaque cursor for the next page in the response." },
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

// SchemaPath is where the schema is served: a static asset overlaid onto the
// portal bundle (assets.go), NOT an /api/ route.
//
// The provider has exactly one authorized tenant route (the query verb), and
// the schema is not tenant data — it is the same document for everyone and is
// public API documentation. Serving it from the Go constant rather than
// copying it into portal/public keeps one source of truth: the same bytes the
// savedview reconciler validates against are the bytes the editor completes
// from. The hub's UI proxy routes any path whose last segment contains a "."
// to this binary, so it reaches the browser exactly like cytoscape.min.js.
const SchemaPath = "/query-schema.json"
