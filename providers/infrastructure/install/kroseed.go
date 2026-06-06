/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// SeedKroCluster applies the Secret the kro-multicluster fork's
// kcp-apiexport provider mounts as its kcp credentials. The chart
// looks for `multicluster.kcp.kubeconfigSecret` in the release
// namespace, key `kubeconfig` — kro then reads the
// APIExportEndpointSlice named by `multicluster.kcp.apiExportEndpointSlice`
// to discover the APIExport's virtual-workspace URL.
//
// The Secret carries a kubeconfig that points at the provider
// workspace with the runtime SA's bearer token, scoped via the
// ClusterRole in install/identity.go. The legacy `kro.run/cluster=true`
// label used by the old kubeconfig provider is intentionally not set:
// the kcp-apiexport provider doesn't watch labeled Secrets, and
// leaving the label on would falsely advertise this Secret to a
// kubeconfig-provider-mode kro install.

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KroNamespace is where the kro fork looks for cluster discovery
// Secrets. Matches the kro Helm chart's default release namespace.
const KroNamespace = "kro-system"

// KroSecretName is the well-known Secret name kro mounts via the
// chart's multicluster.kcp.kubeconfigSecret value. Hardcoded so init,
// the Helm install in the Makefile, and the dev's expectations all
// agree without a configuration knob.
const KroSecretName = "kcp-kubeconfig"

// DefaultDockerHostInternalAddress is what the kubeconfig host gets
// rewritten to when init detects a localhost-bound kcp server. From
// inside a kind cluster (and most Docker / Colima / OrbStack setups
// on macOS + Docker Desktop on Windows) this hostname resolves to
// the host machine. On Linux Docker, kind 0.20+ supports it via
// --add-host=host.docker.internal:host-gateway, which the kind
// command in dev-kro-up arranges.
const DefaultDockerHostInternalAddress = "host.docker.internal"

// SeedKroCluster builds a kubeconfig from id, packages it as a
// kro.run/cluster=true Secret, and applies it into KroNamespace in
// the kro management cluster (resolved via KRO_KUBECONFIG, which the
// existing kro client also reads).
//
// Caller has already verified KRO_KUBECONFIG is set. We re-read it
// here so SeedKroCluster's signature stays focused on the identity
// rather than carrying two configs.
func SeedKroCluster(ctx context.Context, id *RuntimeIdentity) error {
	kroKubeconfig := os.Getenv("KRO_KUBECONFIG")
	if kroKubeconfig == "" {
		return fmt.Errorf("KRO_KUBECONFIG must be set to seed kro")
	}
	kroConfig, err := clientcmd.BuildConfigFromFlags("", kroKubeconfig)
	if err != nil {
		return fmt.Errorf("build kro kubeconfig: %w", err)
	}
	kroCS, err := kubernetes.NewForConfig(kroConfig)
	if err != nil {
		return fmt.Errorf("kro kubernetes client: %w", err)
	}

	kubeconfigYAML, err := encodeKubeconfig(id)
	if err != nil {
		return fmt.Errorf("encode kubeconfig: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      KroSecretName,
			Namespace: KroNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			// kro mounts this Secret at kcp.kubeconfigPath, expecting
			// a `kubeconfig` key (see the chart's deployment.yaml
			// volume mount: items[0].key=kubeconfig).
			"kubeconfig": kubeconfigYAML,
		},
	}

	existing, err := kroCS.CoreV1().Secrets(KroNamespace).Get(ctx, KroSecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = secret.Data
		existing.Labels = secret.Labels
		if _, err = kroCS.CoreV1().Secrets(KroNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return err
		}
	case apierrors.IsNotFound(err):
		if _, err = kroCS.CoreV1().Secrets(KroNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Secret: %w", err)
		}
	default:
		return fmt.Errorf("get existing Secret: %w", err)
	}

	// Bounce the kro Deployment so it picks up the new kubeconfig.
	// kubelet syncs mounted Secrets every ~60s without a restart, but
	// the kro controller doesn't re-read the kubeconfig file at runtime —
	// without a restart the new SA token wouldn't take effect until the
	// next kubelet sync, then only on the first connection retry.
	// Restart is best-effort: a missing Deployment (chart not installed
	// yet) is logged and ignored so init still completes.
	return restartKroDeployment(ctx, kroCS)
}

// restartKroDeployment triggers a rolling restart of the kro Deployment
// in KroNamespace by bumping a pod-template annotation — the standard
// `kubectl rollout restart` recipe, applied via the typed client so we
// don't need kubectl on the host.
func restartKroDeployment(ctx context.Context, cs kubernetes.Interface) error {
	const deployName = "kro"
	dep, err := cs.AppsV1().Deployments(KroNamespace).Get(ctx, deployName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Helm install hasn't created the kro Deployment yet — that's
		// fine, init may run before `dev-kro-up` finishes. The Secret is
		// in place; once the pod starts it'll mount the real kubeconfig.
		return nil
	}
	if err != nil {
		return fmt.Errorf("get kro Deployment: %w", err)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	// init runs without a wall clock (kcp may be embedded; relative time
	// isn't useful for debug). Use the SA token's hash as the rollout
	// trigger — it changes on every successful TokenRequest, which is
	// exactly the signal that we want kro to pick up a new credential.
	dep.Spec.Template.Annotations["kedge.faros.sh/kcp-kubeconfig-revision"] = secretRevision(cs, ctx)
	if _, err := cs.AppsV1().Deployments(KroNamespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("annotate kro Deployment to trigger restart: %w", err)
	}
	return nil
}

// secretRevision returns the kcp-kubeconfig Secret's ResourceVersion so
// the kro Deployment's pod-template annotation flips whenever the
// Secret changes. ResourceVersion is monotonic per-object and bounded,
// so it's a safe rollout-trigger value.
func secretRevision(cs kubernetes.Interface, ctx context.Context) string {
	s, err := cs.CoreV1().Secrets(KroNamespace).Get(ctx, KroSecretName, metav1.GetOptions{})
	if err != nil || s == nil {
		return "unknown"
	}
	return s.ResourceVersion
}

// encodeKubeconfig serializes a clientcmdapi.Config into the YAML
// bytes the kro Secret carries under its `kubeconfig` data key.
//
// The Server URL is rewritten so it resolves from inside the kro pod
// (running on a separate kind cluster); see rewriteHostForKro for
// the heuristic + override env var. TLS is set to InsecureSkipVerify
// because the rewritten hostname (host.docker.internal) usually
// isn't in the apiserver's SAN list — relaxing verification keeps the
// dev loop usable. The bearer token is what gates access; only init
// admins can write this Secret in the first place.
func encodeKubeconfig(id *RuntimeIdentity) ([]byte, error) {
	server, insecure, err := rewriteHostForKro(id.Server, id.CAData)
	if err != nil {
		return nil, fmt.Errorf("rewrite host for kro: %w", err)
	}
	cluster := &clientcmdapi.Cluster{Server: server}
	if insecure {
		cluster.InsecureSkipTLSVerify = true
	} else {
		cluster.CertificateAuthorityData = id.CAData
	}
	cfg := &clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"provider-vw": cluster,
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			id.ServiceAccount: {
				Token: id.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"infrastructure": {
				Cluster:  "provider-vw",
				AuthInfo: id.ServiceAccount,
			},
		},
		CurrentContext: "infrastructure",
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(data, "\n"), nil
}

// rewriteHostForKro rewrites the host portion of a kcp server URL so
// it resolves from inside a kind pod. Order of preference:
//
//  1. INFRASTRUCTURE_KCP_HOST_FOR_KRO env var (verbatim host[:port]
//     replacement) — for setups where neither host.docker.internal nor
//     the LAN IP works.
//  2. Loopback hostnames (127.0.0.1, ::1, localhost) → rewritten to
//     host.docker.internal. From inside a kind container on macOS or
//     Windows Docker Desktop this resolves to the host machine; on
//     Linux Docker it needs --add-host=host.docker.internal:host-gateway
//     on the kind nodes (kind 0.20+ wires this for `extraPortMappings`
//     configurations, but for our default cluster the operator may
//     need to override via INFRASTRUCTURE_KCP_HOST_FOR_KRO).
//  3. Anything else (already a routable hostname) → left alone.
//
// Returns insecure=true when the rewrite makes the server hostname
// differ from the apiserver cert's SAN list (almost always the case
// for host.docker.internal vs a self-signed dev cert), so the kubeconfig
// flips to InsecureSkipTLSVerify.
func rewriteHostForKro(server string, caData []byte) (string, bool, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", false, fmt.Errorf("parse server %q: %w", server, err)
	}
	hostOnly := u.Hostname()
	port := u.Port()

	// Explicit override wins. Format is host or host:port — if just a
	// host, the original port is preserved.
	if override := os.Getenv("INFRASTRUCTURE_KCP_HOST_FOR_KRO"); override != "" {
		h, p, splitErr := net.SplitHostPort(override)
		if splitErr != nil {
			h = override
			p = port
		}
		u.Host = net.JoinHostPort(h, p)
		return u.String(), serverNameMissingFromCA(h, caData), nil
	}

	// Heuristic rewrite for the common dev case: kcp running on the
	// host's loopback, kro running in a kind pod.
	switch hostOnly {
	case "127.0.0.1", "::1", "localhost":
		u.Host = net.JoinHostPort(DefaultDockerHostInternalAddress, port)
		return u.String(), serverNameMissingFromCA(DefaultDockerHostInternalAddress, caData), nil
	}
	return server, false, nil
}

// serverNameMissingFromCA returns true when serverName isn't covered
// by any DNS / IP SAN in the supplied CA bundle. Conservative: when
// caData is empty or can't be parsed we return true (favor insecure
// over a confusing TLS-verify failure inside the kro pod).
func serverNameMissingFromCA(serverName string, caData []byte) bool {
	if len(caData) == 0 {
		return true
	}
	block, _ := pem.Decode(caData)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	// We don't have access to the leaf serving cert here (caData is
	// the CA, not the apiserver's leaf), so we can only check whether
	// the CA itself happens to be valid for the rewritten name. In
	// practice it won't be — kcp's apiserver leaf cert is what carries
	// the SAN list, and our rewritten host isn't in it.
	return cert.VerifyHostname(serverName) != nil
}
