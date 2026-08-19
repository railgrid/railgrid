/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/klog/v2"
)

// The parser is load-bearing for authorization: serveVirtualWorkspace compares
// the token's workspace to the cluster segment this returns, so a path that
// parses loosely is a path that authorizes loosely.
func TestParseVirtualWorkspacePath(t *testing.T) {
	const (
		cluster = "260dym853j73uupr"
		export  = "infrastructure.providers.faros.sh"
	)

	for _, tc := range []struct {
		name        string
		path        string
		wantOK      bool
		wantCluster string
		wantExport  string
	}{{
		name:        "the real watch URL from an APIExportEndpointSlice",
		path:        "/services/apiexport/" + cluster + "/" + export + "/clusters/*/api",
		wantOK:      true,
		wantCluster: cluster,
		wantExport:  export,
	}, {
		name:        "URL-escaped wildcard, as client-go actually sends it",
		path:        "/services/apiexport/" + cluster + "/" + export + "/clusters/%2A/apis/apis.kcp.io/v1alpha1/apibindings",
		wantOK:      true,
		wantCluster: cluster,
		wantExport:  export,
	}, {
		name:        "bare virtual workspace root (discovery)",
		path:        "/services/apiexport/" + cluster + "/" + export,
		wantOK:      true,
		wantCluster: cluster,
		wantExport:  export,
	}, {
		// A workspace path rather than an id — accepted by the segment regex,
		// and still safe because it must equal the token's own claim.
		name:        "colon-separated workspace path",
		path:        "/services/apiexport/root:faros:providers/" + export + "/clusters/*/api",
		wantOK:      true,
		wantCluster: "root:faros:providers",
		wantExport:  export,
	}, {
		name:   "not a virtual workspace path",
		path:   "/clusters/" + cluster + "/api/v1/namespaces",
		wantOK: false,
	}, {
		name:   "prefix collision with another hub service",
		path:   "/services/apiexports-not-really/" + cluster + "/x",
		wantOK: false,
	}, {
		name:   "export segment missing",
		path:   "/services/apiexport/" + cluster,
		wantOK: false,
	}, {
		name:   "empty cluster segment",
		path:   "/services/apiexport//" + export + "/clusters/*/api",
		wantOK: false,
	}, {
		name:   "traversal in the cluster segment",
		path:   "/services/apiexport/../../clusters/root/api",
		wantOK: false,
	}, {
		name:   "traversal in the export segment",
		path:   "/services/apiexport/" + cluster + "/../../../clusters/root/api",
		wantOK: false,
	}, {
		name:   "uppercase cluster is not a kcp id",
		path:   "/services/apiexport/NotACluster/" + export + "/clusters/*/api",
		wantOK: false,
	}, {
		name:   "prefix alone",
		path:   "/services/apiexport",
		wantOK: false,
	}, {
		name:   "prefix with trailing slash only",
		path:   "/services/apiexport/",
		wantOK: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseVirtualWorkspacePath(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				// A rejected parse must yield no cluster: serveVirtualWorkspace
				// would otherwise compare a token claim against "".
				if got.Cluster != "" || got.Export != "" {
					t.Errorf("rejected path still produced %+v", got)
				}
				return
			}
			if got.Cluster != tc.wantCluster {
				t.Errorf("Cluster = %q, want %q", got.Cluster, tc.wantCluster)
			}
			if got.Export != tc.wantExport {
				t.Errorf("Export = %q, want %q", got.Export, tc.wantExport)
			}
		})
	}
}

// vwProxyTo builds a KCPProxy pointed at a stub upstream that records whatever
// reaches it, so a test can tell "denied" from "relayed" by observation rather
// than by restating the rule.
func vwProxyTo(t *testing.T) (*KCPProxy, *upstreamRecord) {
	t.Helper()
	rec := &upstreamRecord{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hit = true
		rec.path = r.URL.Path
		rec.rawPath = r.URL.EscapedPath()
		rec.auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	return &KCPProxy{
		logger:               klog.Background(),
		kcpTarget:            target,
		passthroughTransport: http.DefaultTransport,
	}, rec
}

type upstreamRecord struct {
	hit     bool
	path    string
	rawPath string
	auth    string
}

// The isolation property: a provider's credential reaches its own export's
// virtual workspace and no other. This path grants cross-cluster reach over
// every consumer of an export, so a token that could address a foreign export
// would read another org's workspaces wholesale.
func TestServeVirtualWorkspaceRefusesForeignExports(t *testing.T) {
	const (
		mine   = "260dym853j73uupr"
		theirs = "9xk2p0qwertyuiop"
		export = "infrastructure.providers.faros.sh"
	)

	for _, tc := range []struct {
		name         string
		tokenCluster string
		pathCluster  string
		wantStatus   int
		wantRelayed  bool
	}{{
		name:         "own export is relayed",
		tokenCluster: mine,
		pathCluster:  mine,
		wantStatus:   http.StatusOK,
		wantRelayed:  true,
	}, {
		name:         "another org's export is refused",
		tokenCluster: mine,
		pathCluster:  theirs,
		wantStatus:   http.StatusForbidden,
	}, {
		// Without the explicit empty check, an unclaimed token would compare
		// equal to an empty segment and only the parser would stand between
		// that and a bypass.
		name:         "token carrying no workspace claim is refused",
		tokenCluster: "",
		pathCluster:  mine,
		wantStatus:   http.StatusForbidden,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			p, rec := vwProxyTo(t)
			path := "/services/apiexport/" + tc.pathCluster + "/" + export + "/clusters/*/api"
			vw, ok := parseVirtualWorkspacePath(path)
			if !ok {
				t.Fatalf("test path did not parse: %s", path)
			}

			w := httptest.NewRecorder()
			p.serveVirtualWorkspace(w, httptest.NewRequest(http.MethodGet, path, nil), "tok", tc.tokenCluster, vw)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if rec.hit != tc.wantRelayed {
				t.Fatalf("upstream hit = %v, want %v — a denied request must never reach kcp", rec.hit, tc.wantRelayed)
			}
		})
	}
}

// kcp routes everything after {export} itself, including the /clusters/*
// wildcard that makes this a cross-cluster watch. Rewriting the path — as the
// general ServiceAccount branch does — would break it.
func TestServeVirtualWorkspaceRelaysPathVerbatim(t *testing.T) {
	const cluster = "260dym853j73uupr"
	p, rec := vwProxyTo(t)

	path := "/services/apiexport/" + cluster + "/infrastructure.providers.faros.sh/clusters/%2A/apis/apis.kcp.io/v1alpha1/apibindings"
	vw, ok := parseVirtualWorkspacePath(path)
	if !ok {
		t.Fatalf("test path did not parse: %s", path)
	}

	w := httptest.NewRecorder()
	p.serveVirtualWorkspace(w, httptest.NewRequest(http.MethodGet, path, nil), "provider-token", cluster, vw)

	if !rec.hit {
		t.Fatal("request never reached the upstream")
	}
	if rec.rawPath != path {
		t.Errorf("upstream saw path %q, want %q", rec.rawPath, path)
	}
	// The provider's own credential must arrive intact: kcp authorizes the
	// relayed request natively, so the hub narrows access rather than
	// replacing kcp's own check.
	if rec.auth != "Bearer provider-token" {
		t.Errorf("Authorization = %q, want the caller's own token", rec.auth)
	}
}
