/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

// testCallers is the providerCallers the tests wire: conformance.FakeCallers
// for the gate and the tenant client, plus a fixed export virtual-workspace
// base for the verbs App Studio calls on other providers.
type testCallers struct {
	*conformance.FakeCallers
	// exportBase stands in for this provider's export virtual-workspace URL.
	exportBase string
	// http answers the cross-provider verb calls; nil is http.DefaultClient.
	http *http.Client
}

func (c *testCallers) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error) {
	return dataplane.SubresourceURL(c.exportBase, gvr.Group, gvr.Version, r)
}

func (c *testCallers) ProviderHTTPClient() (*http.Client, error) {
	if c.http != nil {
		return c.http, nil
	}
	return http.DefaultClient, nil
}

// testExportBase is the export virtual-workspace URL the cross-provider tests
// expect verbs to be addressed under.
const testExportBase = "https://hub.example/services/apiexport/root:railgrid:providers/ai.railgrid.ai"

// newTestCallers wraps fake with the cross-provider half, addressed under
// exportBase (testExportBase when empty).
func newTestCallers(fake *conformance.FakeCallers, exportBase string) *testCallers {
	if fake == nil {
		fake = &conformance.FakeCallers{Cluster: "cluster-a", User: "test-user"}
	}
	if exportBase == "" {
		exportBase = testExportBase
	}
	return &testCallers{FakeCallers: fake, exportBase: exportBase}
}

// testVerbPath renders the kube path of one of this provider's verbs, as a
// kcp shard forwards it.
func testVerbPath(cluster, resource, name, verb string, tail ...string) string {
	path := "/clusters/" + cluster + "/apis/" + aiv1alpha1.GroupName + "/" + aiv1alpha1.Version + "/" + resource + "/" + name + "/" + verb
	if len(tail) > 0 {
		path += "/" + strings.Join(tail, "/")
	}
	return path
}

// testUserForToken maps the bearer a test used to type to the username that
// bearer reviewed as, so a test that stamps the caller keeps the actor it had:
// "test-token" and "token" were test-user; anything else is "<user>-token".
func testUserForToken(token string) string {
	switch token {
	case "test-token", "token":
		return "test-user"
	}
	return strings.TrimSuffix(token, "-token")
}

// stampTestCaller stamps user onto r the way a kcp shard does — the
// X-Remote-User header the affinity layer and identityFromRequest read — and
// puts the same identity in the request context the way serve's adapter
// would, so the request is a caller both before and after the adapter.
func stampTestCaller(r *http.Request, user string) *http.Request {
	r.Header.Set(dataplane.HeaderRemoteUser, user)
	r.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	identity := dataplane.ProxiedIdentity{User: user, Groups: []string{"system:authenticated"}}
	return r.WithContext(dataplane.WithProxiedIdentity(r.Context(), identity))
}

// stampTestRoute records the parsed kube route on r the way serve's adapter
// does, so a handler driven without the server sees the route it would have.
func stampTestRoute(r *http.Request) *http.Request {
	route, err := dataplane.ParseSubresourceRequest(r)
	if err != nil {
		return r
	}
	return r.WithContext(dataplane.WithRoute(r.Context(), route))
}
