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
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
	farosclient "github.com/faroshq/faros/pkg/client"
)

// The dev edge: the hub kind cluster joins ITSELF as a KubernetesCluster edge
// (the same cluster hosts the hub, the providers and the agent). It is the
// getting-started flow — enable edges, `faros edge create`, install the
// faros-agent chart with the join token — run non-interactively against the
// static dev user's default workspace.
const (
	devEdgeAgentNamespace = "faros-agent"
	devEdgeAgentRelease   = "faros-agent"
	devEdgeTimeout        = 3 * time.Minute
)

// devEdgeEnabled reports whether the dev edge is set up: it needs the edges
// provider in the cluster and the static-token automation.
func (o *DevOptions) devEdgeEnabled() bool {
	return o.WithEdge && o.providerSelected("edges") && o.providerAutomationEnabled()
}

// registerDevEdge enables the edges provider in the dev user's default
// workspace, creates the KubernetesCluster edge, and installs the
// faros-agent chart into the hub kind cluster with the edge's join token.
// Idempotent: an existing edge is kept and an existing agent release is left
// alone (its join token was redeemed on first connect).
func (o *DevOptions) registerDevEdge(ctx context.Context, restConfig *rest.Config) error {
	api := newDevHubAPI(o.hubLocalURL(), devStaticToken())
	if err := api.waitReady(ctx, o.WaitForReadyTimeout); err != nil {
		return err
	}
	if _, err := api.tokenLogin(ctx, devEdgeTimeout); err != nil {
		return fmt.Errorf("logging into the hub with the dev token: %w", err)
	}

	org, ws, err := waitForDefaultWorkspace(ctx, api, devEdgeTimeout)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Enabling the edges provider in workspace %q of organization %q...\n", ws.DisplayName, org.DisplayName)
	if err := enableProviderWithRetry(ctx, api, org.UUID, ws.UUID, "edges", devEdgeTimeout); err != nil {
		return err
	}

	// The user's workspace, reached through the hub's kcp proxy as the dev
	// user — the same thing `faros edge create` does with the login kubeconfig.
	wsConfig := &rest.Config{
		Host:            apiurl.HubServerURL(o.hubLocalURL(), ws.ClusterName),
		BearerToken:     devStaticToken(),
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
	dyn, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating workspace client: %w", err)
	}

	created, err := createDevEdge(ctx, dyn, o.EdgeName, devEdgeTimeout)
	if err != nil {
		return err
	}
	if created {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Edge %q created\n", o.EdgeName)
	} else {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Edge %q already exists\n", o.EdgeName)
	}

	actionConfig, err := newHelmActionConfig(restConfig, devEdgeAgentNamespace)
	if err != nil {
		return err
	}
	if !created && helmReleaseExists(actionConfig, devEdgeAgentRelease) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Agent release %s already installed, skipping\n", devEdgeAgentRelease)
		return o.waitForDevEdgeReady(ctx, dyn)
	}

	joinToken, err := waitForJoinToken(ctx, dyn, o.EdgeName, devEdgeTimeout)
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	hubSvc, err := clientset.CoreV1().Services(devHubNamespace).Get(ctx, devHubReleaseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("looking up the hub Service: %w", err)
	}
	if hubSvc.Spec.ClusterIP == "" || hubSvc.Spec.ClusterIP == "None" {
		return fmt.Errorf("hub Service %s/%s has no ClusterIP", devHubNamespace, devHubReleaseName)
	}

	values := map[string]any{
		"image": map[string]any{
			"tag":        o.Tag,
			"pullPolicy": "IfNotPresent",
		},
		"agent": map[string]any{
			"edgeName": o.EdgeName,
			// Token bootstrap has no kubeconfig to infer the workspace from.
			"cluster": ws.ClusterName,
			"labels":  map[string]any{"env": "dev"},
			"hub": map[string]any{
				// The kubeconfig the hub hands back after the token exchange
				// points at the edges provider's hub.externalURL (devHubHost).
				// Inside the cluster that name is answered by the hostAlias
				// below (and by CoreDNS when the apps gateway is installed),
				// so both the bootstrap and every later reconnect go to the
				// hub Service.
				"url":                   o.hubExternalURL(),
				"token":                 joinToken,
				"insecureSkipTLSVerify": true,
			},
		},
		"hostAliases": []map[string]any{{
			"ip":        hubSvc.Spec.ClusterIP,
			"hostnames": []string{devHubHost},
		}},
	}

	chartObj, err := loadChart(actionConfig, o.AgentChartPath, o.ChartVersion)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Installing the faros-agent for edge %q into %s/%s...\n", o.EdgeName, devEdgeAgentNamespace, devEdgeAgentRelease)
	if err := helmInstallOrUpgrade(actionConfig, devEdgeAgentNamespace, devEdgeAgentRelease, chartObj, values, devEdgeTimeout); err != nil {
		return err
	}
	return o.waitForDevEdgeReady(ctx, dyn)
}

// waitForDefaultWorkspace polls until the dev user's org bootstrap has
// materialized a workspace with a kcp cluster. Prefers the personal org.
func waitForDefaultWorkspace(ctx context.Context, api *devHubAPI, timeout time.Duration) (devOrg, devWorkspace, error) {
	var org devOrg
	var ws devWorkspace
	var lastErr error
	err := pollUntil(ctx, 2*time.Second, timeout, func(ctx context.Context) (bool, error) {
		orgs, err := api.listOrgs(ctx)
		if err != nil {
			lastErr = err
			return false, nil
		}
		// Personal org first: that is where `faros login` lands the user.
		sort.SliceStable(orgs, func(i, j int) bool { return orgs[i].Personal && !orgs[j].Personal })
		for _, o := range orgs {
			wss, err := api.listWorkspaces(ctx, o.UUID)
			if err != nil {
				lastErr = err
				continue
			}
			for _, w := range wss {
				if w.ClusterName != "" {
					org, ws = o, w
					return true, nil
				}
			}
		}
		return false, nil
	}, func() error {
		return fmt.Errorf("the dev user's default workspace was not provisioned in time (last error: %v)", lastErr)
	})
	return org, ws, err
}

// enableProviderWithRetry enables the provider in the workspace as soon as
// the hub has registered it with an APIExport, accepting all declared claims
// and hub capabilities. It deliberately does not wait for the provider to be
// Ready: kcp publishes an APIExportEndpointSlice endpoint only once the
// export has a consumer (an APIBinding), and providers whose readiness probes
// the virtual workspace (infrastructure) stay unready until then — so waiting
// for Ready before the first binding never ends. 409 means the provider's
// workspace is still being provisioned or a dependency is not enabled yet,
// 404 that the catalog has not caught up: both are retried, like server
// errors.
func enableProviderWithRetry(ctx context.Context, api *devHubAPI, orgUUID, wsUUID, name string, timeout time.Duration) error {
	var last string
	return pollUntil(ctx, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		prov, err := findCatalogProvider(ctx, api, orgUUID, wsUUID, name)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		if prov == nil || prov.APIExportName == "" {
			last = "provider " + name + " is not registered on the hub yet"
			return false, nil
		}
		status, err := api.enableProvider(ctx, orgUUID, wsUUID, *prov)
		if err != nil {
			last = err.Error()
			if status == http.StatusConflict || status == http.StatusNotFound || status == 0 || status >= 500 {
				return false, nil
			}
			return false, fmt.Errorf("enabling provider %s: %w", name, err)
		}
		return true, nil
	}, func() error {
		return fmt.Errorf("enabling provider %s: %s", name, last)
	})
}

// waitForProviderReady waits until the hub reports the provider Ready and
// returns the last readiness message when it does not get there in time.
func waitForProviderReady(ctx context.Context, api *devHubAPI, orgUUID, wsUUID, name string, timeout time.Duration) (string, error) {
	var last string
	err := pollUntil(ctx, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		prov, err := findCatalogProvider(ctx, api, orgUUID, wsUUID, name)
		switch {
		case err != nil:
			last = err.Error()
		case prov == nil:
			last = "not registered on the hub"
		case prov.Ready:
			return true, nil
		default:
			last = prov.ReadinessMessage
		}
		return false, nil
	}, func() error { return fmt.Errorf("provider %s is not ready: %s", name, last) })
	return last, err
}

func findCatalogProvider(ctx context.Context, api *devHubAPI, orgUUID, wsUUID, name string) (*devCatalogProvider, error) {
	providers, err := api.listProviders(ctx, orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}
	for i := range providers {
		if providers[i].Name == name {
			return &providers[i], nil
		}
	}
	return nil, nil
}

// createDevEdge creates the KubernetesCluster, retrying while the freshly
// bound edges API is not discoverable yet. Reports whether it created it.
func createDevEdge(ctx context.Context, dyn dynamic.Interface, name string, timeout time.Duration) (bool, error) {
	gvr := farosclient.EdgeGVRForType("kubernetes")
	edge := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gvr.Group + "/" + gvr.Version,
		"kind":       farosclient.EdgeKindForType("kubernetes"),
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{"env": "dev"},
		},
		"spec": map[string]interface{}{},
	}}
	created := false
	var lastErr error
	err := pollUntil(ctx, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		_, err := dyn.Resource(gvr).Create(ctx, edge, metav1.CreateOptions{})
		switch {
		case err == nil:
			created = true
			return true, nil
		case apierrors.IsAlreadyExists(err):
			return true, nil
		default:
			// NotFound / discovery errors while the APIBinding settles.
			lastErr = err
			return false, nil
		}
	}, func() error {
		return fmt.Errorf("creating edge %q: %w", name, lastErr)
	})
	return created, err
}

// waitForJoinToken polls status.joinToken, which the edges provider's token
// reconciler stamps once it engages the workspace.
func waitForJoinToken(ctx context.Context, dyn dynamic.Interface, name string, timeout time.Duration) (string, error) {
	gvr := farosclient.EdgeGVRForType("kubernetes")
	var token string
	var lastErr error
	err := pollUntil(ctx, 2*time.Second, timeout, func(ctx context.Context) (bool, error) {
		edge, err := dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			lastErr = err
			return false, nil
		}
		t, _, _ := unstructured.NestedString(edge.Object, "status", "joinToken")
		if t == "" {
			return false, nil
		}
		token = t
		return true, nil
	}, func() error {
		return fmt.Errorf("edge %q got no join token in time — is the edges provider running? (last error: %v)", name, lastErr)
	})
	return token, err
}

// waitForDevEdgeReady waits for the agent to connect (status.phase Ready).
// A timeout is reported as a warning, not an error: the environment is
// usable and `faros edge list` shows the live state.
func (o *DevOptions) waitForDevEdgeReady(ctx context.Context, dyn dynamic.Interface) error {
	gvr := farosclient.EdgeGVRForType("kubernetes")
	var phase string
	err := pollUntil(ctx, 3*time.Second, devEdgeTimeout, func(ctx context.Context) (bool, error) {
		edge, err := dyn.Resource(gvr).Get(ctx, o.EdgeName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		phase, _, _ = unstructured.NestedString(edge.Object, "status", "phase")
		return phase == "Ready", nil
	}, func() error {
		return fmt.Errorf("edge %q is not Ready yet (phase %q)", o.EdgeName, phase)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: %v. Check with: kubectl --kubeconfig %s.kubeconfig -n %s logs deploy/%s\n", err, o.HubClusterName, devEdgeAgentNamespace, devEdgeAgentRelease)
		return nil
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Edge %q is Ready\n", o.EdgeName)
	return nil
}
