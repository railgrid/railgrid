/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package server holds the one route this provider serves that is its own:
//
//	(a) data-plane verb  POST /dataplane/clusters/{id}/greetings/{name}/greet
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
	// Callers builds the per-request, caller-scoped client the two gates run
	// through. Nil makes the greet verb fail closed; the provider logs the
	// missing kubeconfig at startup.
	Callers dataplane.CallerFactory
	// Greetings is the GVR the verb hangs off.
	Greetings schema.GroupVersionResource
}

// NewDataPlane returns the handler for everything under /dataplane/. It is
// mounted as serve.Options.DataPlane, which dispatches off the raw request
// path, so the grammar's refusals (".." , "//", a workspace path where a
// logical-cluster ID belongs) happen in dataplane.ParseRequest rather than
// being rewritten by a mux first.
func NewDataPlane(deps Deps) http.Handler { return &dataPlane{deps: deps} }

type dataPlane struct{ deps Deps }
