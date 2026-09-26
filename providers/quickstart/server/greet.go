/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
)

// greetVerb is the one verb this provider serves. It is also, verbatim, the
// custom subresource the APIExport publishes and the noun RBAC grants: a
// consumer's rule is `create` on `greetings/greet`.
const greetVerb = "greet"

// greetLimits bound the verb. A data-plane verb declares its own limits rather
// than inheriting a server-wide default, so a slow or chatty verb cannot take
// the whole provider down with it.
var greetLimits = dataplane.Limits{
	Timeout:        10 * time.Second,
	MaxInputBytes:  4 << 10,
	MaxOutputBytes: 16 << 10,
}

// greetResult is the verb's response body, wrapped by the actionwire envelope.
type greetResult struct {
	Greeting string `json:"greeting"`
}

// ServeHTTP serves the greet verb as kcp forwards it:
//
//	POST /clusters/{clusterID}/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/{name}/greet
//
// and is the one place a new provider author sees the gate written out. Read
// it top to bottom; everything a data-plane verb must do is here and nothing
// else is.
func (s *dataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. The route. serve's subresource adapter is the one parser: it has
	//    already refused a traversal segment, a percent-encoded separator, a
	//    workspace path where a logical-cluster ID belongs, and any coordinate
	//    the manifest does not declare, and it put what it parsed in the
	//    request context. A request that arrives with no route did not come
	//    through the adapter and is refused, whatever its URL says. The verb
	//    takes no component and nothing beneath itself.
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || route.Resource != s.deps.Greetings.Resource || route.Component != "" || route.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	if route.Verb != greetVerb {
		// The adapter only dispatches declared coordinates, so this is belt
		// and braces: a verb this handler does not implement is not served.
		http.NotFound(w, r)
		return
	}

	// 2. The gate. kcp already authenticated the caller and authorized
	//    `create` on greetings/greet with ordinary RBAC before it forwarded
	//    the request at all; there is no bearer here and the verb grant is not
	//    repeated. What the gate settles is VISIBILITY: the caller must be
	//    able to see greetings/{name} in {clusterID}. The provider holds the
	//    caller's name and groups and no credential to read with, so Gate
	//    runs a SubjectAccessReview for `get` on the object on the caller's
	//    behalf, then reads it AS THE PROVIDER through its APIExport virtual
	//    workspace — the one door where the provider has standing in a tenant
	//    workspace. That is what makes "workspace A's caller cannot greet
	//    workspace B's Greeting" true, and it hands the object back so the
	//    verb reads spec from what the caller was entitled to see.
	//
	//    A foreign provider (another export that claimed greetings/greet,
	//    calling through its own virtual workspace) is authorized by the
	//    claim kcp already enforced; Gate reads the parent and asks no review.
	greeting, _, err := dataplane.Gate(r.Context(), s.deps.Callers, s.deps.Greetings, route.Request)
	if err != nil {
		// The detail stays here. WriteError answers with the status text and
		// nothing else, so a denial cannot tell a caller whether the object
		// exists.
		log.Printf("greet refused: %v", err)
		dataplane.WriteError(w, err)
		return
	}

	// 3. Serve. dataplane.Serve owns the rest of the response path: POST only,
	//    the input limit, a strict {"input": …} body, the timeout, the output
	//    limits, and the actionwire envelope on success and on failure alike.
	//    Write nothing to w after this point.
	//
	//    The caller's name comes from the identity kcp stamped, the same one
	//    Gate decided with. It labels the greeting; it authorizes nothing.
	var user string
	if identity, ok := dataplane.ProxiedIdentityFrom(r.Context()); ok {
		user = identity.User
	}
	env := actionwire.New(r, "quickstart", route.Verb, actionwire.ResourceRef{
		APIVersion: s.deps.Greetings.GroupVersion().String(),
		Kind:       "Greeting",
		Resource:   s.deps.Greetings.Resource,
		Name:       route.Name,
	})
	dataplane.Serve(w, r, env, greetLimits, func(_ context.Context, _ json.RawMessage) (any, *actionwire.Error) {
		return greet(greeting, user)
	})
}

// greet is the whole of the verb: render the message the tenant stored on the
// object for the caller kcp authenticated. It takes the object the gate
// returned, never a fresh read — the point of the gate handing the object back
// is that the verb acts on what the caller was entitled to see.
func greet(greeting *unstructured.Unstructured, user string) (any, *actionwire.Error) {
	message, _, err := unstructured.NestedString(greeting.Object, "spec", "message")
	if err != nil || message == "" {
		// A greeting with no message is not a server fault; it is a Greeting
		// whose Ready condition already says so. Not retryable.
		return nil, &actionwire.Error{
			Code:    "message_empty",
			Message: "greeting has no spec.message yet",
		}
	}
	if user == "" {
		// Gate refuses a request with no caller, so this is unreachable on a
		// served request; it keeps the label well-formed regardless.
		user = "anonymous"
	}
	return greetResult{Greeting: message + ", " + user}, nil
}
