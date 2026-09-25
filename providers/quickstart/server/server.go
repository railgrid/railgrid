/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package server holds the one route this provider serves that is its own:
//
//	(a) data-plane verb  POST /clusters/{id}/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/{name}/greet
//
// A verb is a kcp custom subresource: the APIExport declares "greetings/greet",
// kcp authorizes the caller with ordinary RBAC and reverse-proxies the request
// here with the caller's identity stamped in requestheader headers. There is no
// other way to reach it — no hub-proxied spelling, no bearer.
//
// Everything else about the HTTP surface — /healthz, /readyz, the portal and
// the request log — is provider-sdk/serve's, and main.go wires the two
// together. The route list is closed
// (docs/provider-connectivity-contract.md §"Pillar 2 route classes"); serve
// refuses anything that is not one of the classes, so there is no /api/* here
// and no way to add one. If the portal needs to list, create or edit a
// Greeting it does that against kcp at /clusters/{id} with the kube client —
// a backend route that mirrors a CR is a deviation even when it is authorized
// correctly.
package server

import (
	"net/http"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

// Deps are the collaborators the verb needs. Both are injected so the handler
// can be built in a test with fakes — see server_test.go, which drives this
// exact handler, mounted in a real serve.New server, through the shared
// data-plane conformance suite.
type Deps struct {
	// Callers builds the client the gate acts through: AS THE PROVIDER, via
	// its APIExport virtual workspace, because the shard stamps the caller's
	// identity but hands over no credential. Nil makes the greet verb fail
	// closed; the provider logs the missing kubeconfig at startup.
	Callers dataplane.ProviderCallerFactory
	// Greetings is the GVR the verb hangs off.
	Greetings schema.GroupVersionResource
}

// NewDataPlane returns the handler for the greet verb. It is mounted as
// serve.Options.DataPlane and reached only through serve.Options.Subresources:
// serve's adapter parses the shard-forwarded path off the raw URL (so the
// grammar's refusals — "..", "//", a workspace path where a logical-cluster ID
// belongs — happen before any mux can rewrite them), checks the coordinate
// against the manifest, and dispatches here with the parsed route and the
// stamped caller in the request context.
func NewDataPlane(deps Deps) http.Handler { return &dataPlane{deps: deps} }

type dataPlane struct{ deps Deps }
