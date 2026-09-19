// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package index owns the two naming decisions the kuery SQL index and the
// rest of the provider have to agree on: what an engaged edge's cluster row is
// called, and what label carries the tenant on it.
//
// It is its own package because both halves of the provider need them and
// neither may import the other: the engagement controller writes them, the
// query path reads them, and the query path must not pull a controller (and
// its multicluster manager) into the request path.
//
// Neither is authority. The authority for "which edges may this caller query"
// is the Engagement set in kuery's own workspace; these are how that answer is
// expressed to a store that only understands one cluster name or one label
// map per query.
package index

import "strings"

// TenantLabel is the label kuery cluster rows carry, whose value is the tenant
// workspace's kcp logical-cluster ID.
//
// Deliberately a bare identifier: kuery's SQLite dialect compiles label
// filters to json_extract(cl.labels, '$.{key}'), where a dot or a slash in the
// key would be parsed as a JSON path segment.
const TenantLabel = "tenant"

// StoreName is the name kuery records one engaged edge's cluster row under:
// "{tenantClusterID}/{edge}". It is the identifier shared by the engaged map,
// the store row, the query filter and the Engagement spec, and it is derived
// purely from values a delete still has.
func StoreName(cluster, edge string) string { return cluster + "/" + edge }

// SplitStoreName is StoreName's inverse. A name with no separator is treated
// as a bare cluster with an empty edge.
func SplitStoreName(storeName string) (cluster, edge string) {
	cluster, edge, _ = strings.Cut(storeName, "/")
	return cluster, edge
}

// EdgeOf returns the bare edge name from a store name, or the input unchanged
// when it carries no prefix. It is what turns an objects[].cluster value in a
// query result back into the edge name a tenant recognises.
func EdgeOf(storeName string) string {
	if i := strings.LastIndex(storeName, "/"); i >= 0 {
		return storeName[i+1:]
	}
	return storeName
}
