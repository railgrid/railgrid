/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestSelectedProviders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flag    []string
		want    []string
		wantErr string
	}{
		{name: "default", flag: NewDevOptions(genericclioptions.IOStreams{}).Providers, want: []string{"edges", "infrastructure", "code", "agents", "app-studio"}},
		{name: "install order, duplicates and blanks dropped", flag: []string{"quickstart", " edges", "", "quickstart"}, want: []string{"edges", "quickstart"}},
		{name: "requirements pulled in", flag: []string{"app-studio"}, want: []string{"infrastructure", "app-studio"}},
		{name: "empty disables", flag: []string{""}, want: nil},
		{name: "unknown", flag: []string{"edges", "kuery"}, wantErr: `unknown provider "kuery"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &DevOptions{Providers: tc.flag}
			specs, err := o.selectedProviders()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range specs {
				got = append(got, s.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Install and enable run in devProviderSpecs order, so every requirement
// must come before the provider that needs it.
func TestDevProviderSpecsOrderedByRequirements(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range devProviderSpecs {
		for _, dep := range s.Requires {
			if !seen[dep] {
				t.Errorf("%s requires %s, which is not listed before it", s.Name, dep)
			}
		}
		seen[s.Name] = true
	}
	for _, name := range devDefaultProviders {
		if !seen[name] {
			t.Errorf("default provider %s has no spec", name)
		}
	}
}

func TestProviderValuesWireDatabaseAndKubeconfig(t *testing.T) {
	o := NewDevOptions(genericclioptions.IOStreams{})
	for _, name := range []string{"agents", "app-studio"} {
		spec, _ := devProviderSpecByName(name)
		if spec.Database == "" {
			t.Fatalf("%s should get a database", name)
		}
		env := devProviderEnv{DatabaseURL: devDatabaseURL(name, spec.Database)}
		store, _ := spec.Values(o, env)["store"].(map[string]any)
		if store["databaseURL"] != env.DatabaseURL {
			t.Errorf("%s: store.databaseURL = %v, want %s", name, store["databaseURL"], env.DatabaseURL)
		}
	}
	if got := devDatabaseURL("agents", "agents"); got != "postgres://railgrid:railgrid-dev@agents-db.railgrid-providers.svc.cluster.local:5432/agents?sslmode=disable" {
		t.Errorf("devDatabaseURL = %s", got)
	}

	infra, _ := devProviderSpecByName("infrastructure")
	vals := infra.Values(o, devProviderEnv{KubeconfigSecret: "railgrid-infrastructure-kubeconfig"})
	op, _ := vals["operator"].(map[string]any)
	secret, _ := op["providerKubeconfigSecret"].(map[string]any)
	if op["enabled"] != true || secret["name"] != "railgrid-infrastructure-kubeconfig" {
		t.Errorf("infrastructure must run in operator mode on the minted kubeconfig, got %v", op)
	}
	if ce, _ := vals["catalogEntry"].(map[string]any); ce["enabled"] != false {
		t.Error("in operator mode the operator registers the CatalogEntry; the chart copy must be off")
	}
}

func TestCodeGitHubOAuthFromEnv(t *testing.T) {
	o := NewDevOptions(genericclioptions.IOStreams{})
	code, _ := devProviderSpecByName("code")

	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	if vals := code.Values(o, devProviderEnv{}); vals != nil {
		t.Fatalf("without credentials GitHub OAuth must stay off, got %v", vals)
	}

	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "id")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "secret")
	gh, _ := code.Values(o, devProviderEnv{})["githubOAuth"].(map[string]any)
	if gh["enabled"] != true || gh["clientId"] != "id" {
		t.Fatalf("githubOAuth = %v", gh)
	}
	if gh["redirectURL"] != "https://console.127.0.0.1.sslip.io:9443/services/providers/code/oauth/github/callback" {
		t.Errorf("redirectURL = %v", gh["redirectURL"])
	}
	if ref, _ := gh["clientSecretRef"].(map[string]any); ref["name"] != devCodeOAuthSecret {
		t.Errorf("clientSecretRef = %v", ref)
	}
}

func TestDevEdgeEnabled(t *testing.T) {
	o := NewDevOptions(genericclioptions.IOStreams{})
	if !o.devEdgeEnabled() {
		t.Fatal("the default environment should register the dev edge")
	}
	o.Providers = []string{"quickstart"}
	if o.devEdgeEnabled() {
		t.Fatal("the dev edge needs the edges provider")
	}
	o = NewDevOptions(genericclioptions.IOStreams{})
	o.WithDex = true
	if o.devEdgeEnabled() || o.providerAutomationEnabled() {
		t.Fatal("--with-dex disables token login, so the automation must be off")
	}
}

func TestMergeValues(t *testing.T) {
	dst := map[string]any{
		"hub":     map[string]any{"url": "a", "insecure": true},
		"devMode": false,
	}
	mergeValues(dst, map[string]any{
		"hub":     map[string]any{"externalURL": "b"},
		"devMode": true,
	})
	want := map[string]any{
		"hub":     map[string]any{"url": "a", "insecure": true, "externalURL": "b"},
		"devMode": true,
	}
	if !reflect.DeepEqual(dst, want) {
		t.Fatalf("got %v, want %v", dst, want)
	}
}

func TestHubAdminValues(t *testing.T) {
	o := NewDevOptions(genericclioptions.IOStreams{})
	hub := map[string]any{}
	o.hubAdminValues(hub)
	if hub["internalURL"] != "https://railgrid-hub.railgrid-system.svc.cluster.local:9443" {
		t.Fatalf("internalURL = %v", hub["internalURL"])
	}
	if !reflect.DeepEqual(hub["adminUsers"], []string{devStaticAdminUser()}) {
		t.Fatalf("adminUsers = %v", hub["adminUsers"])
	}
	// The hub's certificate comes from the dev CA (stable across re-runs),
	// not from the chart's per-render self-signed CA.
	tls, _ := hub["tls"].(map[string]any)
	self, _ := tls["selfSigned"].(map[string]any)
	cm, _ := tls["certManager"].(map[string]any)
	issuer, _ := cm["issuerRef"].(map[string]any)
	if self["enabled"] != false || cm["enabled"] != true || issuer["name"] != devCAName || issuer["kind"] != "ClusterIssuer" {
		t.Fatalf("hub.tls = %v", tls)
	}
	if names, _ := cm["dnsNames"].([]string); len(names) == 0 || names[0] != devHubHost {
		t.Fatalf("hub.tls.certManager.dnsNames = %v, want the browser host first", cm["dnsNames"])
	}
	if got := o.hubExternalURL(); got != "https://console.127.0.0.1.sslip.io:9443" {
		t.Fatalf("hubExternalURL = %s", got)
	}
}

// The identity the hub synthesizes for a static token must match what the
// automation puts on --admin-users, or every /api/admin call is refused. It
// must not contain the token: --admin-users ends up in the hub's Helm values.
func TestDevStaticAdminUserMatchesToken(t *testing.T) {
	if got, want := devStaticAdminUser(), "railgrid:static:47b9dce0e91570a1"; got != want {
		t.Fatalf("devStaticAdminUser() = %q, want %q (see identity.NewStaticToken)", got, want)
	}
	if strings.Contains(devStaticAdminUser(), devStaticToken()) {
		t.Fatalf("devStaticAdminUser() = %q contains the token", devStaticAdminUser())
	}
}

func TestPinEmbeddedShardURL(t *testing.T) {
	older := &chart.Chart{Values: map[string]any{"kcp": map[string]any{"embedded": map[string]any{"securePort": 6443}}}}
	hub := map[string]any{}
	pinEmbeddedShardURL(older, hub)
	want := []string{
		"--kcp-shard-external-url=" + devKCPShardURL,
		"--kcp-shard-virtual-workspace-url=" + devKCPShardURL,
	}
	if !reflect.DeepEqual(hub["extraArgs"], want) {
		t.Fatalf("older chart: extraArgs = %v, want %v", hub["extraArgs"], want)
	}

	modelled := &chart.Chart{Values: map[string]any{"kcp": map[string]any{"embedded": map[string]any{"shardURL": ""}}}}
	hub = map[string]any{}
	pinEmbeddedShardURL(modelled, hub)
	if _, ok := hub["extraArgs"]; ok {
		t.Fatal("a chart that models kcp.embedded.shardURL must not get the flags twice (it rejects repeated flags)")
	}
}

// The in-repo hub chart, installed the way `railgrid dev init` installs it,
// must advertise the same stable shard URL the CLI pins for older charts.
func TestHubChartRendersDevShardURL(t *testing.T) {
	ch, err := loader.Load("../../../../../deploy/charts/railgrid-hub")
	if err != nil {
		t.Fatal(err)
	}
	vals, err := chartutil.ToRenderValues(ch, map[string]any{
		"hub": map[string]any{"hubExternalURL": "https://console.127.0.0.1.sslip.io:9443"},
	}, chartutil.ReleaseOptions{Name: devHubReleaseName, Namespace: devHubNamespace}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := engine.Render(ch, vals)
	if err != nil {
		t.Fatal(err)
	}
	workload := out["railgrid-hub/templates/workload.yaml"]
	for _, flag := range []string{"--kcp-shard-external-url=", "--kcp-shard-virtual-workspace-url="} {
		if !strings.Contains(workload, flag+devKCPShardURL) {
			t.Errorf("workload does not render %s%s", flag, devKCPShardURL)
		}
	}
	if !strings.Contains(out["railgrid-hub/templates/service-kcp.yaml"], "publishNotReadyAddresses: true") {
		t.Error("the headless kcp Service must publish not-ready addresses")
	}
}

func TestDevCoreDNSBlockIdempotent(t *testing.T) {
	corefile := `.:53 {
    errors
    health {
       lameduck 5s
    }
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
    }
    forward . /etc/resolv.conf
    reload
}
`
	first, err := withDevCoreDNSBlock(corefile, devCoreDNSBlock("10.96.0.10", "10.96.0.20"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, `IN A 10.96.0.10"`) || !strings.Contains(first, `IN A 10.96.0.20"`) ||
		!strings.Contains(first, `match ^console\.127\.0\.0\.1\.sslip\.io\.$`) {
		t.Fatalf("block missing:\n%s", first)
	}
	if strings.Index(first, devCoreDNSMarkerStart) < strings.Index(first, ".:53 {") {
		t.Fatal("block must be inside the root server block")
	}
	// Re-running with new IPs replaces the block instead of stacking another.
	second, err := withDevCoreDNSBlock(first, devCoreDNSBlock("10.96.0.11", "10.96.0.20"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(second, devCoreDNSMarkerStart+"\n") != 1 || strings.Contains(second, "10.96.0.10") {
		t.Fatalf("block not replaced:\n%s", second)
	}
	if _, err := withDevCoreDNSBlock("example.org:53 {\n}\n", devCoreDNSBlock("a", "b")); err == nil {
		t.Fatal("a Corefile without a root block must be rejected")
	}
}

func TestAppsValues(t *testing.T) {
	o := NewDevOptions(genericclioptions.IOStreams{})
	hub := map[string]any{}
	o.hubAdminValues(hub)
	if hub["publishedAppsDomain"] != devAppsBaseDomain {
		t.Errorf("publishedAppsDomain = %v", hub["publishedAppsDomain"])
	}
	if !reflect.DeepEqual(hub["portalFrameSources"], []string{"https://*.apps.127.0.0.1.sslip.io:10443"}) {
		t.Errorf("portalFrameSources = %v", hub["portalFrameSources"])
	}

	infra, _ := devProviderSpecByName("infrastructure")
	op, _ := infra.Values(o, devProviderEnv{KubeconfigSecret: "s"})["operator"].(map[string]any)
	pub, _ := op["publishing"].(map[string]any)
	if pub["baseDomain"] != devAppsBaseDomain || pub["publicPort"] != 10443 || pub["hubPublicURL"] != "https://console.127.0.0.1.sslip.io:9443" {
		t.Errorf("operator.publishing = %v", pub)
	}
	app, _ := op["application"].(map[string]any)
	if gw, _ := app["gateway"].(map[string]any); gw["name"] != devAppsGatewayName || gw["namespace"] != devAppsNamespace {
		t.Errorf("operator.application = %v", app)
	}

	// Without infrastructure there is nothing to publish.
	o.Providers = []string{"edges"}
	hub = map[string]any{}
	o.hubAdminValues(hub)
	if _, ok := hub["publishedAppsDomain"]; ok {
		t.Error("publishedAppsDomain set without the infrastructure provider")
	}
}

func TestResyncEndpointSlices(t *testing.T) {
	slice := func(name string, endpoints ...string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha1",
			"kind":       "APIExportEndpointSlice",
			"metadata":   map[string]any{"name": name},
		}}
		if len(endpoints) > 0 {
			var eps []any
			for _, e := range endpoints {
				eps = append(eps, map[string]any{"url": e})
			}
			_ = unstructured.SetNestedSlice(u.Object, eps, "status", "endpoints")
		}
		return u
	}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{apiExportEndpointSliceGVR: "APIExportEndpointSliceList"},
		slice("ai.railgrid.ai"), slice("edges.providers.railgrid.ai", "https://shard/services/apiexport/x/edges"))
	res := dyn.Resource(apiExportEndpointSliceGVR)

	// Nothing publishes an endpoint in the fake, so the call times out — but
	// only the empty slice may have been touched.
	err := resyncEndpointSlices(context.Background(), res, time.Millisecond, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ai.railgrid.ai") || strings.Contains(err.Error(), "edges") {
		t.Fatalf("err = %v, want only ai.railgrid.ai pending", err)
	}
	ai, _ := res.Get(context.Background(), "ai.railgrid.ai", metav1.GetOptions{})
	if ai.GetAnnotations()[devSliceResyncAnnotation] == "" {
		t.Error("the slice without endpoints was not touched")
	}
	edges, _ := res.Get(context.Background(), "edges.providers.railgrid.ai", metav1.GetOptions{})
	if _, ok := edges.GetAnnotations()[devSliceResyncAnnotation]; ok {
		t.Error("a slice that already has an endpoint must be left alone")
	}

	// Once every slice has an endpoint the resync is done.
	if _, err := res.Update(context.Background(), slice("ai.railgrid.ai", "https://shard/services/apiexport/y/ai"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := resyncEndpointSlices(context.Background(), res, time.Millisecond, time.Second); err != nil {
		t.Fatalf("resync with all endpoints published: %v", err)
	}
}

func TestRehostURL(t *testing.T) {
	got, err := rehostURL("https://railgrid-hub.railgrid-system.svc.cluster.local:9443/clusters/4a0d9qnjz8vqgjh6", "https://127.0.0.1:9443")
	if err != nil || got != "https://127.0.0.1:9443/clusters/4a0d9qnjz8vqgjh6" {
		t.Fatalf("rehostURL = %q, %v", got, err)
	}
}

func TestAppsCertificateUsesDevCA(t *testing.T) {
	m := appsGatewayManifests()
	if !strings.Contains(m, "name: "+devCAName+"\n    kind: ClusterIssuer") {
		t.Fatalf("the apps certificate must be issued by the dev CA:\n%s", m)
	}
}
