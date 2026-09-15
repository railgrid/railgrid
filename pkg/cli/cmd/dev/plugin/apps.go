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
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The apps gateway: what the infrastructure provider's templates expose
// workloads through. Every template that publishes an app (browser, searxng,
// simple-webapp, application) renders an HTTPRoute and waits for a Gateway
// controller to accept it, so without Gateway API + a controller kro rejects
// those graphs and their instances never get past Pending.
//
// Apps are served under *.apps.127.0.0.1.sslip.io, next to the hub at
// console.127.0.0.1.sslip.io (see devHubHost): public DNS answers every
// *.127.0.0.1.sslip.io name with 127.0.0.1, so nothing needs /etc/hosts, and
// the portal and the apps share one site, so the private-app sign-in cookies
// set in App Studio's preview iframe are first-party, as in production.
//
//	browser → https://<app>.apps.127.0.0.1.sslip.io:<AppsHTTPSPort>
//	        → kind hostPort → node port devAppsNodePort → Envoy (listener
//	          devAppsListenerPort) → HTTPRoute → the template's access gate
//
// Inside the cluster 127.0.0.1 is the pod itself, so CoreDNS answers the app
// zone and the hub host with the Envoy and hub Service IPs instead. These are
// ordinary DNS names, so every client honours that (unlike *.localhost, which
// curl and Chromium pin to loopback).
const (
	devAppsBaseDomain     = "apps.127.0.0.1.sslip.io"
	devAppsNamespace      = "envoy-gateway-system"
	devAppsGatewayName    = "railgrid-apps"
	devAppsTLSSecret      = "railgrid-apps-tls"
	devAppsListenerPort   = 10443
	devAppsNodePort       = 30443
	devEnvoyChartRef      = "oci://docker.io/envoyproxy/gateway-helm"
	devEnvoyChartVersion  = "v1.7.0" // same as the kcp Tilt stack
	devEnvoyReleaseName   = "envoy"
	devCoreDNSMarkerStart = "# railgrid-dev-dns"
	devCoreDNSMarkerEnd   = "# railgrid-dev-dns-end"
)

// appsGatewayEnabled: only the infrastructure provider publishes apps.
func (o *DevOptions) appsGatewayEnabled() bool {
	return o.providerSelected("infrastructure")
}

// appsPublicURLSuffix is ":<port>" when the apps port is not 443.
func (o *DevOptions) appsPublicURLSuffix() string {
	if o.AppsHTTPSPort == 443 {
		return ""
	}
	return fmt.Sprintf(":%d", o.AppsHTTPSPort)
}

// appsHubValues are the hub settings published apps need: the zone private
// app sign-in may redirect to, and permission for the portal to frame app
// hosts (App Studio's development preview).
func (o *DevOptions) appsHubValues(hubValues map[string]any) {
	if !o.appsGatewayEnabled() {
		return
	}
	hubValues["publishedAppsDomain"] = devAppsBaseDomain
	hubValues["portalFrameSources"] = []string{"https://*." + devAppsBaseDomain + o.appsPublicURLSuffix()}
}

// appsInfrastructureValues wires the infrastructure operator to the gateway.
func (o *DevOptions) appsInfrastructureValues() map[string]any {
	gateway := map[string]any{"name": devAppsGatewayName, "namespace": devAppsNamespace}
	return map[string]any{
		"application": map[string]any{
			"baseDomain": devAppsBaseDomain,
			"gateway":    gateway,
		},
		"publishing": map[string]any{
			"baseDomain": devAppsBaseDomain,
			"gateway":    gateway,
			// The access gate talks to the hub in-cluster; visitors are
			// redirected to the hub's browser address.
			"hubURL":       o.hubInternalURL(),
			"hubPublicURL": o.hubExternalURL(),
			"insecure":     true,
			"publicScheme": "https",
			"publicPort":   o.AppsHTTPSPort,
		},
	}
}

// installAppsGateway installs Envoy Gateway (which brings the Gateway API
// CRDs), a NodePort-backed GatewayClass, a wildcard certificate and the
// railgrid-apps Gateway, then points in-cluster DNS at it. Idempotent.
func (o *DevOptions) installAppsGateway(ctx context.Context, restConfig *rest.Config, kubeconfigPath string) error {
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Installing Envoy Gateway for apps under *.%s...\n", devAppsBaseDomain)
	actionConfig, err := newHelmActionConfig(restConfig, devAppsNamespace)
	if err != nil {
		return err
	}
	chartObj, err := loadChart(actionConfig, devEnvoyChartRef, devEnvoyChartVersion)
	if err != nil {
		return err
	}
	if err := helmInstallOrUpgrade(actionConfig, devAppsNamespace, devEnvoyReleaseName, chartObj, map[string]any{}, devProviderInstallTimeout); err != nil {
		return fmt.Errorf("envoy gateway: %w", err)
	}

	if err := kubectlApply(ctx, kubeconfigPath, appsGatewayManifests()); err != nil {
		return fmt.Errorf("applying the apps gateway: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	// Envoy Gateway names the data-plane Service after a hash; find it by the
	// owning-gateway label once the Gateway is reconciled.
	var envoyIP string
	if err := pollUntil(ctx, 3*time.Second, devProviderInstallTimeout, func(ctx context.Context) (bool, error) {
		svcs, err := clientset.CoreV1().Services(devAppsNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "gateway.envoyproxy.io/owning-gateway-name=" + devAppsGatewayName,
		})
		if err != nil || len(svcs.Items) == 0 {
			return false, nil
		}
		envoyIP = svcs.Items[0].Spec.ClusterIP
		return envoyIP != "" && envoyIP != "None", nil
	}, func() error {
		return fmt.Errorf("envoy did not create a Service for Gateway %s/%s", devAppsNamespace, devAppsGatewayName)
	}); err != nil {
		return err
	}
	hubSvc, err := clientset.CoreV1().Services(devHubNamespace).Get(ctx, devHubReleaseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("looking up the hub Service: %w", err)
	}
	if err := ensureDevCoreDNS(ctx, clientset, envoyIP, hubSvc.Spec.ClusterIP); err != nil {
		return fmt.Errorf("configuring in-cluster DNS for apps: %w", err)
	}

	if !o.hubNodeHasPort(ctx, devAppsNodePort) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: kind cluster %s was created without the apps port mapping, so apps are reachable only inside the cluster. Recreate it (railgrid dev delete && railgrid dev init) to reach them at https://<app>.%s%s\n", o.HubClusterName, devAppsBaseDomain, o.appsPublicURLSuffix())
	}
	return nil
}

// appsGatewayManifests renders the GatewayClass (NodePort data plane on a
// fixed node port, so kind can map it to the host), the wildcard certificate
// (from the dev CA, so trusting <cluster>-ca.crt covers apps too) and the
// Gateway.
func appsGatewayManifests() string {
	return fmt.Sprintf(`apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        type: NodePort
        patch:
          type: StrategicMerge
          value:
            spec:
              ports:
                - port: %[3]d
                  nodePort: %[4]d
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: %[1]s
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: %[1]s
    namespace: %[2]s
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: %[5]s
  namespace: %[2]s
spec:
  secretName: %[5]s
  dnsNames:
    - "*.%[6]s"
  issuerRef:
    name: %[7]s
    kind: ClusterIssuer
    group: cert-manager.io
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  gatewayClassName: %[1]s
  listeners:
    - name: apps-https
      hostname: "*.%[6]s"
      port: %[3]d
      protocol: HTTPS
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: %[5]s
      allowedRoutes:
        namespaces:
          from: All
`, devAppsGatewayName, devAppsNamespace, devAppsListenerPort, devAppsNodePort,
		devAppsTLSSecret, devAppsBaseDomain, devCAName)
}

func kubectlApply(ctx context.Context, kubeconfigPath, manifests string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifests)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// devCoreDNSBlock answers the app zone with the Envoy Service IP and the
// hub's browser host with the hub Service IP, for pods only (the host keeps
// resolving both to loopback). Kept between markers so re-runs replace it.
func devCoreDNSBlock(envoyIP, hubIP string) string {
	return fmt.Sprintf(`    %[1]s
    template IN A {
        match ^([^.]+\.)+%[2]s\.$
        answer "{{ .Name }} 60 IN A %[3]s"
        fallthrough
    }
    template IN A {
        match ^%[4]s\.$
        answer "{{ .Name }} 60 IN A %[5]s"
        fallthrough
    }
    %[6]s
`, devCoreDNSMarkerStart, regexp.QuoteMeta(devAppsBaseDomain), envoyIP,
		regexp.QuoteMeta(devHubHost), hubIP, devCoreDNSMarkerEnd)
}

// withDevCoreDNSBlock returns corefile with the managed block (re)placed at
// the top of the root ".:53" server block.
func withDevCoreDNSBlock(corefile, block string) (string, error) {
	var kept []string
	skipping := false
	for _, line := range strings.Split(corefile, "\n") {
		switch strings.TrimSpace(line) {
		case devCoreDNSMarkerStart:
			skipping = true
			continue
		case devCoreDNSMarkerEnd:
			skipping = false
			continue
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	for i, line := range kept {
		if strings.HasPrefix(strings.TrimSpace(line), ".:53") && strings.HasSuffix(strings.TrimSpace(line), "{") {
			out := append([]string{}, kept[:i+1]...)
			out = append(out, strings.TrimRight(block, "\n"))
			out = append(out, kept[i+1:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	return "", fmt.Errorf("no root server block (.:53) in the CoreDNS Corefile")
}

func ensureDevCoreDNS(ctx context.Context, clientset kubernetes.Interface, envoyIP, hubIP string) error {
	cms := clientset.CoreV1().ConfigMaps("kube-system")
	cm, err := cms.Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		return err
	}
	updated, err := withDevCoreDNSBlock(cm.Data["Corefile"], devCoreDNSBlock(envoyIP, hubIP))
	if err != nil {
		return err
	}
	if updated == cm.Data["Corefile"] {
		return nil
	}
	cm.Data["Corefile"] = updated
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return err
	}
	// Restart rather than wait for the reload plugin to notice the mounted
	// ConfigMap change (up to a couple of minutes).
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"railgrid.ai/dev-dns-restart":%q}}}}}`, time.Now().Format(time.RFC3339))
	_, err = clientset.AppsV1().Deployments("kube-system").Patch(ctx, "coredns", types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// hubNodeHasPort reports whether the hub kind node publishes containerPort to
// the host. Errors count as "yes" so a Docker hiccup does not print a false
// warning.
func (o *DevOptions) hubNodeHasPort(ctx context.Context, containerPort int) bool {
	dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return true
	}
	defer func() { _ = dc.Close() }()
	info, err := dc.ContainerInspect(ctx, o.HubClusterName+"-control-plane")
	if err != nil || info.HostConfig == nil {
		return true
	}
	for port := range info.HostConfig.PortBindings {
		if port.Int() == containerPort {
			return true
		}
	}
	return false
}
