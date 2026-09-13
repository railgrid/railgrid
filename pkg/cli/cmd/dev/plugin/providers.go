/*
Copyright 2026 The Faros Authors.

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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	pkgversion "github.com/faroshq/faros/pkg/version"
)

// Providers are installed INTO the hub kind cluster, next to the hub, so a
// `faros dev init` environment is usable out of the box (edges, at minimum —
// without it there is nothing to connect a cluster to). The flow mirrors the
// production onboarding path rather than the Tilt one:
//
//  1. POST /api/admin/providers          — the hub's Provider controller
//     provisions root:faros:providers:<name>, a ServiceAccount and its
//     kubeconfig Secret. Needs the static dev user on --admin-users.
//  2. GET  /api/admin/providers/<name>/kubeconfig?server=internal — the
//     minted kubeconfig re-pointed at the hub's in-cluster Service.
//  3. helm install providers/<name>/deploy/chart — the chart's init container
//     bootstraps the APIExport and applies the CatalogEntry (registration)
//     through that kubeconfig; the serve container heartbeats with the
//     static dev token.
const (
	devHubNamespace      = "faros-system"
	devHubReleaseName    = "faros-hub"
	devProvidersNS       = "faros-providers"
	devProviderTokenName = "faros-provider-hub-token"

	// devProviderChartRepo is the OCI base every provider chart is published
	// under (see .github/workflows/provider-release.yaml).
	devProviderChartRepo = "oci://ghcr.io/faroshq/charts"

	// devStaticAdminUser is the identity the hub synthesizes for the first
	// dev static token: static-<sanitized token>@faros.local (see
	// sanitizeTokenSlug in pkg/server/proxy). --admin-users matches on it,
	// which is what lets `faros dev init` call /api/admin/* with dev-token.
	devStaticAdminUser = "static-dev-token@faros.local"

	// Provider images are large (App Studio ships a browser) and the
	// infrastructure operator installs kro before its serve pods start, so a
	// cold kind node needs a while.
	devProviderInstallTimeout = 10 * time.Minute
	// devProviderReadyTimeout bounds the wait for an enabled provider to
	// report Ready (its virtual-workspace endpoint appears after the first
	// binding, then the next health check passes).
	devProviderReadyTimeout = 3 * time.Minute
)

// devProviderSpec describes one provider `faros dev init` knows how to run in
// the hub kind cluster from its published chart. Providers that need
// external credentials or services to do anything (code, databricks, kuery,
// linear) are left to the Tilt stack.
type devProviderSpec struct {
	Name        string
	DisplayName string
	// Chart is the chart name under devProviderChartRepo; the in-repo source
	// is providers/<Name>/deploy/chart.
	Chart string
	// Requires lists providers that must be installed (and enabled in a
	// workspace) first. Selecting this provider pulls them in.
	Requires []string
	// Database, when set, gets the provider its own Postgres in the
	// providers namespace; the connection URL is passed to Values.
	Database string
	// Prepare, when set, runs before the chart install (e.g. to create a
	// Secret the values reference).
	Prepare func(ctx context.Context, o *DevOptions, clientset kubernetes.Interface) error
	// Values returns provider-specific chart values merged over the common
	// ones (fullnameOverride, hub wiring, kubeconfig Secret, CatalogEntry).
	Values func(o *DevOptions, env devProviderEnv) map[string]any
}

// devProviderEnv is what the install step prepared for one provider.
type devProviderEnv struct {
	// KubeconfigSecret holds the minted provider kubeconfig under key
	// "kubeconfig".
	KubeconfigSecret string
	// DatabaseURL is set for providers with a Database.
	DatabaseURL string
}

// devProviderSpecs is in install order: a provider comes after everything it
// Requires.
var devProviderSpecs = []devProviderSpec{
	{
		Name:        "edges",
		DisplayName: "Edges",
		Chart:       "faros-edges-provider",
		Values: func(o *DevOptions, _ devProviderEnv) map[string]any {
			return map[string]any{
				"devMode": true,
				"hub": map[string]any{
					// Baked into every agent kubeconfig the provider mints:
					// agents on the laptop reach the hub at the host-mapped
					// port; the in-cluster dev agent resolves the same name
					// through a hostAlias (see edge.go).
					"externalURL": o.hubExternalURL(),
					"internalURL": o.hubInternalURL(),
				},
			}
		},
	},
	{
		// Operator mode, as production runs it: the chart installs only the
		// operator, which bootstraps the provider workspace through the
		// minted kubeconfig, helm-installs kro into this cluster, runs the
		// serve Deployment in namespace faros-infrastructure-provider and
		// registers the CatalogEntry itself. Apps are published through the
		// Envoy Gateway installed beforehand (apps.go).
		Name:        "infrastructure",
		DisplayName: "Infrastructure",
		Chart:       "faros-infrastructure-provider",
		Values: func(o *DevOptions, env devProviderEnv) map[string]any {
			operator := map[string]any{
				"enabled": true,
				// kro's chart installs CRDs and cluster RBAC.
				"clusterAdmin": true,
				"providerKubeconfigSecret": map[string]any{
					"name": env.KubeconfigSecret,
					"key":  "kubeconfig",
				},
				"provider": map[string]any{"replicas": 1},
			}
			// Apps are exposed through the faros-apps Gateway (apps.go).
			mergeValues(operator, o.appsInfrastructureValues())
			return map[string]any{
				// The operator registers the CatalogEntry from its embedded
				// manifest; the chart's ConfigMap copy is for the init flow.
				"catalogEntry": map[string]any{"enabled": false},
				"operator":     operator,
			}
		},
	},
	{
		// Git repository management. GitHub sign-in is wired only when
		// GITHUB_OAUTH_CLIENT_ID/_SECRET are set (see devCodeGitHubOAuth);
		// tenants can always use token-based Connections.
		Name:        "code",
		DisplayName: "Code",
		Chart:       "faros-code-provider",
		Prepare: func(ctx context.Context, _ *DevOptions, clientset kubernetes.Interface) error {
			oauth, ok := devCodeGitHubOAuth()
			if !ok {
				return nil
			}
			return ensureSecret(ctx, clientset, devProvidersNS, devCodeOAuthSecret, map[string][]byte{"clientSecret": []byte(oauth.clientSecret)})
		},
		Values: func(o *DevOptions, _ devProviderEnv) map[string]any {
			oauth, ok := devCodeGitHubOAuth()
			if !ok {
				return nil
			}
			return map[string]any{
				"githubOAuth": map[string]any{
					"enabled":         true,
					"clientId":        oauth.clientID,
					"clientSecretRef": map[string]any{"name": devCodeOAuthSecret, "key": "clientSecret"},
					// Through the hub's /services proxy, which the browser
					// reaches at the host-mapped port. Register this exact
					// callback on the GitHub OAuth App.
					"redirectURL":  o.hubExternalURL() + "/services/providers/code/oauth/github/callback",
					"portalOrigin": o.hubExternalURL(),
				},
			}
		},
	},
	{
		Name:        "agents",
		DisplayName: "Agents",
		Chart:       "faros-agents-provider",
		Database:    "agents",
		Values: func(_ *DevOptions, env devProviderEnv) map[string]any {
			return map[string]any{
				"store": map[string]any{"databaseURL": env.DatabaseURL},
			}
		},
	},
	{
		Name:        "app-studio",
		DisplayName: "App Studio",
		Chart:       "faros-app-studio-provider",
		// Its CatalogEntry declares infrastructure as a dependency: a
		// workspace cannot enable App Studio without it.
		Requires: []string{"infrastructure"},
		Database: "appstudio",
		Values: func(o *DevOptions, env devProviderEnv) map[string]any {
			return map[string]any{
				"store": map[string]any{"databaseURL": env.DatabaseURL},
				"hub":   map[string]any{"publicURL": o.hubExternalURL()},
				// The preview bridge needs a signing key pair generated at
				// install time; without it App Studio runs in its documented
				// degraded mode (no signed preview annotations).
				"previewBridge": map[string]any{"enabled": false},
			}
		},
	},
	{
		Name:        "quickstart",
		DisplayName: "Quickstart",
		Chart:       "faros-quickstart-provider",
	},
}

// devDefaultProviders is the --providers default.
var devDefaultProviders = []string{"edges", "infrastructure", "code", "agents", "app-studio"}

// devCodeOAuthSecret holds the code provider's GitHub OAuth client secret.
const devCodeOAuthSecret = "code-github-oauth"

type devGitHubOAuth struct{ clientID, clientSecret string }

// devCodeGitHubOAuth reads the code provider's GitHub OAuth App credentials
// from the CLI's environment — the names providers/code/.env uses — so a
// secret never has to go on the command line.
func devCodeGitHubOAuth() (devGitHubOAuth, bool) {
	id, secret := os.Getenv("GITHUB_OAUTH_CLIENT_ID"), os.Getenv("GITHUB_OAUTH_CLIENT_SECRET")
	return devGitHubOAuth{clientID: id, clientSecret: secret}, id != "" && secret != ""
}

func devProviderNames() []string {
	names := make([]string, 0, len(devProviderSpecs))
	for _, s := range devProviderSpecs {
		names = append(names, s.Name)
	}
	return names
}

func devProviderSpecByName(name string) (devProviderSpec, bool) {
	for _, s := range devProviderSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return devProviderSpec{}, false
}

// selectedProviders resolves --providers against the known specs, adds what
// they require, and returns them in install order. An empty flag disables
// provider installation.
func (o *DevOptions) selectedProviders() ([]devProviderSpec, error) {
	want := map[string]bool{}
	var add func(name, requiredBy string) error
	add = func(name, requiredBy string) error {
		if want[name] {
			return nil
		}
		spec, ok := devProviderSpecByName(name)
		if !ok {
			if requiredBy != "" {
				return fmt.Errorf("provider %q requires unknown provider %q", requiredBy, name)
			}
			return fmt.Errorf("unknown provider %q for --providers (supported: %s)", name, strings.Join(devProviderNames(), ", "))
		}
		want[name] = true
		for _, dep := range spec.Requires {
			if err := add(dep, name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, raw := range o.Providers {
		if name := strings.TrimSpace(raw); name != "" {
			if err := add(name, ""); err != nil {
				return nil, err
			}
		}
	}
	var out []devProviderSpec
	for _, s := range devProviderSpecs {
		if want[s.Name] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (o *DevOptions) providerSelected(name string) bool {
	specs, err := o.selectedProviders()
	if err != nil {
		return false
	}
	for _, s := range specs {
		if s.Name == name {
			return true
		}
	}
	return false
}

// devHubHost is the local hub's browser/CLI host name. Public DNS answers
// every *.127.0.0.1.sslip.io name with 127.0.0.1, so it reaches the kind
// host-port mapping with no /etc/hosts entry, and it shares a site with the
// apps zone (devAppsBaseDomain). The Tilt stacks use the same host.
const devHubHost = "console.127.0.0.1.sslip.io"

// hubExternalURL is the browser/CLI address of the local hub.
func (o *DevOptions) hubExternalURL() string {
	return fmt.Sprintf("https://%s:%d", devHubHost, o.HubHTTPSPort)
}

// hubLocalURL is how this process reaches the hub: the kind host-port
// mapping on loopback, so it does not depend on DNS at all. The hub's
// self-signed cert is not verified.
func (o *DevOptions) hubLocalURL() string {
	return fmt.Sprintf("https://127.0.0.1:%d", o.HubHTTPSPort)
}

// hubInternalURL is the hub's in-cluster Service. The chart names the
// Service after the release (faros-hub) and serves on the hub port.
func (o *DevOptions) hubInternalURL() string {
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", devHubReleaseName, devHubNamespace, o.HubHTTPSPort)
}

// providerAutomationEnabled reports whether the in-cluster provider + edge
// automation can run. It drives the hub's admin API with the static dev
// token, so it needs token login — which the --with-dex hub disables.
func (o *DevOptions) providerAutomationEnabled() bool {
	return !o.WithDex
}

// hubAdminValues are the extra hub chart values the provider automation
// depends on: the in-cluster URL minted provider kubeconfigs point at, and
// the static dev identity allowed to call /api/admin/*.
func (o *DevOptions) hubAdminValues(hubValues map[string]any) {
	hubValues["internalURL"] = o.hubInternalURL()
	// Serving certificate from the dev CA (ca.go), valid for the browser host.
	hubValues["tls"] = devHubTLSValues()
	admins := []string{devStaticAdminUser}
	if o.WithDex {
		admins = append(admins, "admin@test.faros.local")
	}
	hubValues["adminUsers"] = admins
	o.appsHubValues(hubValues)
}

// devKCPShardURL is the embedded kcp's stable shard URL: the hub pod's
// headless-Service DNS name (StatefulSet faros-hub, Service faros-hub-kcp).
const devKCPShardURL = "https://" + devHubReleaseName + "-0." + devHubReleaseName + "-kcp." + devHubNamespace + ".svc.cluster.local:6443"

// pinEmbeddedShardURL makes an embedded-kcp hub advertise a shard URL that
// survives pod restarts. Left to kcp it is the pod IP, and kcp does not
// refresh APIExportEndpointSlices when it changes, so after any hub restart
// (a values change on re-run, `faros dev update`, a Docker restart) the hub's
// controllers and the providers watch a dead address and nothing reconciles.
// Charts that model kcp.embedded.shardURL already render the stable name;
// older published charts get the same flags through hub.extraArgs.
func pinEmbeddedShardURL(chartObj *chart.Chart, hubValues map[string]any) {
	if kcpValues, ok := chartObj.Values["kcp"].(map[string]any); ok {
		if embedded, ok := kcpValues["embedded"].(map[string]any); ok {
			if _, modelled := embedded["shardURL"]; modelled {
				return
			}
		}
	}
	hubValues["extraArgs"] = []string{
		"--kcp-shard-external-url=" + devKCPShardURL,
		"--kcp-shard-virtual-workspace-url=" + devKCPShardURL,
	}
}

// ---------------------------------------------------------------------------
// hub REST client (static dev token)
// ---------------------------------------------------------------------------

type devHubAPI struct {
	base  string
	token string
	http  *http.Client
}

func newDevHubAPI(base, token string) *devHubAPI {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // local self-signed dev hub
	return &devHubAPI{base: base, token: token, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}
}

// do issues one request and decodes a JSON body into out when it is non-nil.
// The status code is returned alongside so callers can treat 404/409 as
// "not yet" without string matching.
func (c *devHubAPI) do(ctx context.Context, method, path string, headers map[string]string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "faros-cli/"+pkgversion.Get())
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if raw, ok := out.(*[]byte); ok {
			*raw = data
			return resp.StatusCode, nil
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.StatusCode, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
			}
		}
	}
	return resp.StatusCode, nil
}

// waitReady blocks until /healthz answers.
func (c *devHubAPI) waitReady(ctx context.Context, timeout time.Duration) error {
	var last error
	return pollUntil(ctx, 2*time.Second, timeout, func(ctx context.Context) (bool, error) {
		_, err := c.do(ctx, http.MethodGet, "/healthz", nil, nil, nil)
		last = err
		return err == nil, nil
	}, func() error { return fmt.Errorf("hub at %s not ready: %v", c.base, last) })
}

// tokenLogin provisions the static-token user (and, through the org
// bootstrap controller, its personal org + default workspace) and returns
// the login response. A freshly started hub answers /healthz and passes its
// readiness probe before the tenant APIs are served, so server errors are
// retried until timeout.
func (c *devHubAPI) tokenLogin(ctx context.Context, timeout time.Duration) (*tenancyv1alpha1.LoginResponse, error) {
	var resp tenancyv1alpha1.LoginResponse
	var lastErr error
	err := pollUntil(ctx, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		status, err := c.do(ctx, http.MethodPost, "/auth/token-login", nil, nil, &resp)
		if err == nil {
			return true, nil
		}
		lastErr = err
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			return false, err // wrong token, token login disabled: not transient
		}
		return false, nil
	}, func() error { return fmt.Errorf("hub token login did not succeed in time: %w", lastErr) })
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *devHubAPI) createProvider(ctx context.Context, name, displayName string) error {
	body := map[string]string{"name": name, "displayName": displayName}
	_, err := c.do(ctx, http.MethodPost, "/api/admin/providers", nil, body, nil)
	return err
}

// providerKubeconfig fetches the minted kubeconfig re-pointed at the hub's
// in-cluster Service. found=false while the Provider controller has not
// written the Secret yet.
func (c *devHubAPI) providerKubeconfig(ctx context.Context, name string) (kubeconfig []byte, found bool, err error) {
	var raw []byte
	status, err := c.do(ctx, http.MethodGet, "/api/admin/providers/"+name+"/kubeconfig?server=internal", nil, nil, &raw)
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

type devOrg struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	Personal    bool   `json:"personal"`
}

type devWorkspace struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	ClusterName string `json:"clusterName"`
}

type devListResponse[T any] struct {
	Items []T `json:"items"`
}

func (c *devHubAPI) listOrgs(ctx context.Context) ([]devOrg, error) {
	var resp devListResponse[devOrg]
	if _, err := c.do(ctx, http.MethodGet, "/api/orgs", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *devHubAPI) listWorkspaces(ctx context.Context, orgUUID string) ([]devWorkspace, error) {
	var resp devListResponse[devWorkspace]
	headers := map[string]string{"X-Faros-Org": orgUUID}
	if _, err := c.do(ctx, http.MethodGet, "/api/orgs/"+orgUUID+"/workspaces", headers, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

type devCatalogClaim struct {
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
}

type devHubAccess struct {
	Capability string `json:"capability"`
	Scope      string `json:"scope"`
}

type devCatalogProvider struct {
	Name             string            `json:"name"`
	Ready            bool              `json:"ready"`
	ReadinessMessage string            `json:"readinessMessage,omitempty"`
	APIExportName    string            `json:"apiExportName,omitempty"`
	PermissionClaims []devCatalogClaim `json:"permissionClaims,omitempty"`
	HubAccess        []devHubAccess    `json:"hubAccess,omitempty"`
}

func tenantHeaders(orgUUID, wsUUID string) map[string]string {
	return map[string]string{"X-Faros-Org": orgUUID, "X-Faros-Workspace": wsUUID}
}

func (c *devHubAPI) listProviders(ctx context.Context, orgUUID, wsUUID string) ([]devCatalogProvider, error) {
	var resp devListResponse[devCatalogProvider]
	if _, err := c.do(ctx, http.MethodGet, "/api/providers", tenantHeaders(orgUUID, wsUUID), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// enableProvider creates the APIBinding for the provider in the workspace,
// accepting every claim and hub capability it declares (this is a dev
// environment). The status is returned so the caller can back off on 409
// ("provider workspace not provisioned yet") the same way the portal does.
func (c *devHubAPI) enableProvider(ctx context.Context, orgUUID, wsUUID string, prov devCatalogProvider) (int, error) {
	name := prov.Name
	body := map[string]any{"acceptedClaims": prov.PermissionClaims, "acceptedHubAccess": prov.HubAccess}
	path := fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/%s/enable", orgUUID, wsUUID, name)
	return c.do(ctx, http.MethodPost, path, tenantHeaders(orgUUID, wsUUID), body, nil)
}

// ---------------------------------------------------------------------------
// provider install
// ---------------------------------------------------------------------------

// installProviders onboards and installs every --providers entry into the hub
// kind cluster. Idempotent: re-running upgrades the Helm releases and refreshes
// the kubeconfig Secrets.
func (o *DevOptions) installProviders(ctx context.Context, restConfig *rest.Config) error {
	specs, err := o.selectedProviders()
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}
	if !o.providerAutomationEnabled() {
		_, _ = fmt.Fprint(o.Streams.ErrOut, "Skipping provider installation: --with-dex disables the static dev token the automation signs in with\n")
		return nil
	}

	api := newDevHubAPI(o.hubLocalURL(), devStaticToken())
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Waiting for the hub at %s...\n", o.hubLocalURL())
	if err := api.waitReady(ctx, o.WaitForReadyTimeout); err != nil {
		return err
	}
	// Provisions the static dev user, which --admin-users matches by email.
	if _, err := api.tokenLogin(ctx, devProviderInstallTimeout); err != nil {
		return fmt.Errorf("logging into the hub with the dev token: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	if err := ensureNamespace(ctx, clientset, devProvidersNS); err != nil {
		return err
	}
	if err := ensureSecret(ctx, clientset, devProvidersNS, devProviderTokenName, map[string][]byte{"token": []byte(devStaticToken())}); err != nil {
		return err
	}

	// Templates that publish apps need Gateway API and a controller before
	// kro will accept them, so the gateway goes in ahead of infrastructure.
	if o.appsGatewayEnabled() {
		if err := o.installAppsGateway(ctx, restConfig, fmt.Sprintf("%s.kubeconfig", o.HubClusterName)); err != nil {
			return err
		}
	}

	for _, spec := range specs {
		if err := o.installProvider(ctx, api, clientset, restConfig, spec); err != nil {
			return fmt.Errorf("installing provider %s: %w", spec.Name, err)
		}
	}
	return nil
}

// enableProviders enables every installed provider in the dev user's
// default workspace, in install order so dependencies (infrastructure before
// app-studio) are enabled first, then waits for each to report Ready. A
// provider that is enabled but not Ready in time is a warning, not a
// failure: the environment is usable and the portal shows the live state.
func (o *DevOptions) enableProviders(ctx context.Context, restConfig *rest.Config) error {
	specs, err := o.selectedProviders()
	if err != nil || len(specs) == 0 || !o.EnableProviders || !o.providerAutomationEnabled() {
		return err
	}
	api := newDevHubAPI(o.hubLocalURL(), devStaticToken())
	if _, err := api.tokenLogin(ctx, devProviderInstallTimeout); err != nil {
		return fmt.Errorf("logging into the hub with the dev token: %w", err)
	}
	org, ws, err := waitForDefaultWorkspace(ctx, api, devProviderInstallTimeout)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Enabling provider %s in workspace %q of organization %q...\n", spec.Name, ws.DisplayName, org.DisplayName)
		if err := enableProviderWithRetry(ctx, api, org.UUID, ws.UUID, spec.Name, devProviderInstallTimeout); err != nil {
			return err
		}
	}
	// Make sure each provider's virtual-workspace endpoint is published now
	// that it has a consumer (see resync.go); without it the provider never
	// engages the workspace it was just enabled in.
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	for _, spec := range specs {
		if err := o.resyncProviderEndpointSlices(ctx, clientset, spec.Name); err != nil {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: %v\n", err)
		}
	}
	for _, spec := range specs {
		if msg, err := waitForProviderReady(ctx, api, org.UUID, ws.UUID, spec.Name, devProviderReadyTimeout); err != nil {
			if ctx.Err() != nil {
				return err
			}
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: provider %s is enabled but not Ready yet (%s). Check: kubectl --kubeconfig %s.kubeconfig -n %s get pods\n", spec.Name, msg, o.HubClusterName, devProvidersNS)
			continue
		}
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Provider %s is Ready\n", spec.Name)
	}
	return nil
}

func (o *DevOptions) installProvider(ctx context.Context, api *devHubAPI, clientset kubernetes.Interface, restConfig *rest.Config, spec devProviderSpec) error {
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Onboarding provider %s on the hub...\n", spec.Name)
	if err := api.createProvider(ctx, spec.Name, spec.DisplayName); err != nil {
		return fmt.Errorf("creating Provider: %w", err)
	}

	var kubeconfig []byte
	var lastErr error
	if err := pollUntil(ctx, 2*time.Second, devProviderInstallTimeout, func(ctx context.Context) (bool, error) {
		kc, found, err := api.providerKubeconfig(ctx, spec.Name)
		if err != nil {
			lastErr = err
			return false, nil
		}
		if !found {
			return false, nil
		}
		kubeconfig = kc
		return true, nil
	}, func() error {
		return fmt.Errorf("provider %s kubeconfig was not minted in time (last error: %v)", spec.Name, lastErr)
	}); err != nil {
		return err
	}

	env := devProviderEnv{KubeconfigSecret: "faros-" + spec.Name + "-kubeconfig"}
	if err := ensureSecret(ctx, clientset, devProvidersNS, env.KubeconfigSecret, map[string][]byte{"kubeconfig": kubeconfig}); err != nil {
		return err
	}
	if spec.Database != "" {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Starting Postgres for provider %s...\n", spec.Name)
		url, err := ensureProviderDatabase(ctx, clientset, spec.Name, spec.Database, devProviderInstallTimeout)
		if err != nil {
			return err
		}
		env.DatabaseURL = url
	}

	values := map[string]any{
		"fullnameOverride": spec.Name,
		"replicaCount":     1,
		"image": map[string]any{
			"pullPolicy": "IfNotPresent",
		},
		"hub": map[string]any{
			"url":      o.hubInternalURL(),
			"insecure": true,
			"tokenSecretRef": map[string]any{
				"name": devProviderTokenName,
				"key":  "token",
			},
		},
		"providerKubeconfig": map[string]any{"secretName": env.KubeconfigSecret},
		// The chart renders the CatalogEntry into a ConfigMap its init
		// container applies through the provider kubeconfig — that is the
		// registration step, with ui/backend URLs on the in-cluster Service.
		"catalogEntry": map[string]any{"enabled": true},
	}
	if spec.Prepare != nil {
		if err := spec.Prepare(ctx, o, clientset); err != nil {
			return err
		}
	}
	if spec.Values != nil {
		mergeValues(values, spec.Values(o, env))
	}

	actionConfig, err := newHelmActionConfig(restConfig, devProvidersNS)
	if err != nil {
		return err
	}
	chartObj, imageTag, err := o.loadProviderChart(actionConfig, spec)
	if err != nil {
		return err
	}
	if imageTag != "" {
		values["image"].(map[string]any)["tag"] = imageTag
	}
	shownTag := imageTag
	if shownTag == "" {
		shownTag = chartObj.AppVersion()
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Installing provider %s (chart %s %s, image tag %s) into %s/%s...\n", spec.Name, chartObj.Name(), chartObj.Metadata.Version, shownTag, devProvidersNS, spec.Name)
	if err := helmInstallOrUpgrade(actionConfig, devProvidersNS, spec.Name, chartObj, values, devProviderInstallTimeout); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Provider %s ready\n", spec.Name)
	return nil
}

// loadProviderChart resolves a provider chart from --provider-chart-repo:
// an oci:// base (published charts, latest version unless pinned) or a
// local faros checkout (providers/<name>/deploy/chart). It also returns the
// image tag to set, empty to keep the chart's appVersion.
//
// An in-repo chart carries a placeholder appVersion (0.1.0) that only the
// release workflow stamps, and that tag names an ancient image. So a local
// chart runs the latest published image of the provider unless
// --provider-image-tag says otherwise (e.g. a kind-loaded local build).
func (o *DevOptions) loadProviderChart(actionConfig *action.Configuration, spec devProviderSpec) (*chart.Chart, string, error) {
	repo := strings.TrimRight(o.ProviderChartRepo, "/")
	if !strings.HasPrefix(repo, "oci://") {
		path := filepath.Join(repo, "providers", spec.Name, "deploy", "chart")
		if _, err := os.Stat(filepath.Join(path, "Chart.yaml")); err != nil {
			return nil, "", fmt.Errorf("no chart at %s (is --provider-chart-repo a faros checkout?): %w", path, err)
		}
		chartObj, err := loader.Load(path)
		if err != nil {
			return nil, "", err
		}
		imageTag := o.ProviderImageTag
		if imageTag == "" {
			published := devProviderChartRepo + "/" + spec.Chart
			latest, err := latestOCIChartVersion(actionConfig.RegistryClient, published)
			if err != nil {
				return nil, "", fmt.Errorf("resolving the latest published %s image for the local chart (set one with --provider-image-tag): %w", spec.Name, err)
			}
			// Provider releases tag the image vX.Y.Z for chart version X.Y.Z.
			imageTag = "v" + latest
		}
		return chartObj, imageTag, nil
	}
	ref := repo + "/" + spec.Chart
	version := o.ProviderChartVersion
	if version == "" {
		latest, err := latestOCIChartVersion(actionConfig.RegistryClient, ref)
		if err != nil {
			return nil, "", fmt.Errorf("resolving latest version of %s (pin one with --provider-chart-version): %w", ref, err)
		}
		version = latest
	}
	chartObj, err := loadChart(actionConfig, ref, version)
	return chartObj, o.ProviderImageTag, err
}

// latestOCIChartVersion lists the repository's semver tags (helm returns
// them newest first) and picks the highest stable one.
func latestOCIChartVersion(reg *registry.Client, ref string) (string, error) {
	// The registry client wants a bare reference, without the oci:// scheme.
	tags, err := reg.Tags(strings.TrimPrefix(ref, "oci://"))
	if err != nil {
		return "", err
	}
	for _, t := range tags {
		if !strings.Contains(t, "-") { // skip pre-releases
			return t, nil
		}
	}
	if len(tags) > 0 {
		return tags[0], nil
	}
	return "", fmt.Errorf("no versions published at %s", ref)
}

// ---------------------------------------------------------------------------
// helm + kube helpers shared by providers.go and edge.go
// ---------------------------------------------------------------------------

func newHelmActionConfig(restConfig *rest.Config, namespace string) (*action.Configuration, error) {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(&restConfigGetter{config: restConfig, namespace: namespace}, namespace, "secret", func(string, ...any) {}); err != nil {
		return nil, fmt.Errorf("initialising helm for namespace %s: %w", namespace, err)
	}
	reg, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating helm registry client: %w", err)
	}
	actionConfig.RegistryClient = reg
	return actionConfig, nil
}

// loadChart loads a chart from an oci:// reference (at version) or a local
// path.
func loadChart(actionConfig *action.Configuration, ref, version string) (*chart.Chart, error) {
	if strings.HasPrefix(ref, "oci://") {
		tmp := action.NewInstall(actionConfig)
		tmp.Version = version
		path, err := tmp.LocateChart(ref, cli.New())
		if err != nil {
			return nil, fmt.Errorf("locating chart %s %s: %w", ref, version, err)
		}
		return loader.Load(path)
	}
	return loader.Load(ref)
}

// helmInstallOrUpgrade installs the release, or upgrades it when a release
// with that name already exists, and waits for its workloads to be ready.
func helmInstallOrUpgrade(actionConfig *action.Configuration, namespace, release string, chartObj *chart.Chart, values map[string]any, timeout time.Duration) error {
	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	if _, err := hist.Run(release); err == nil {
		upg := action.NewUpgrade(actionConfig)
		upg.Namespace = namespace
		upg.Wait = true
		upg.Timeout = timeout
		if _, err := upg.Run(release, chartObj, values); err != nil {
			return fmt.Errorf("upgrading release %s: %w", release, err)
		}
		return nil
	}
	inst := action.NewInstall(actionConfig)
	inst.ReleaseName = release
	inst.Namespace = namespace
	inst.CreateNamespace = true
	inst.Wait = true
	inst.Timeout = timeout
	if _, err := inst.Run(chartObj, values); err != nil {
		return fmt.Errorf("installing release %s: %w", release, err)
	}
	return nil
}

func helmReleaseExists(actionConfig *action.Configuration, release string) bool {
	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	_, err := hist.Run(release)
	return err == nil
}

func ensureNamespace(ctx context.Context, clientset kubernetes.Interface, name string) error {
	_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", name, err)
	}
	return nil
}

// ensureSecret creates or replaces an Opaque Secret's data.
func ensureSecret(ctx context.Context, clientset kubernetes.Interface, namespace, name string, data map[string][]byte) error {
	secrets := clientset.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}
	existing.Data = data
	if _, err := secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// mergeValues deep-merges src into dst (nested map[string]any values are
// merged, everything else is replaced).
func mergeValues(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				mergeValues(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

// pollUntil calls cond every interval until it reports done, the context
// ends, or timeout passes — in which case onTimeout's error is returned.
func pollUntil(ctx context.Context, interval, timeout time.Duration, cond func(context.Context) (bool, error), onTimeout func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		done, err := cond(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return onTimeout()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// devStaticToken is the first static dev token; it is what the automation
// signs in with and what provider heartbeats carry.
func devStaticToken() string {
	return devStaticTokens[0]
}
