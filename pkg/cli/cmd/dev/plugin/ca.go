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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The dev CA: one long-lived certificate authority, created once by
// cert-manager, that issues the hub's serving certificate and the apps
// gateway's wildcard certificate. It is exported next to the kubeconfig as
// <hub-cluster-name>-ca.crt so clients that cannot skip TLS verification
// (Codex, browsers you want to stop warning) can trust the local hub and its
// published apps instead:
//
//	railgrid mcp codex --ca-file railgrid-hub-ca.crt
//
// The chart's own self-signed option is no use for this: Helm regenerates that
// CA on every render, so after a re-run the Secret no longer matches the
// certificate the running hub serves.
const (
	devCAName      = "railgrid-dev-ca" // Certificate, Secret and ClusterIssuer
	devCANamespace = "cert-manager"    // a CA ClusterIssuer reads its Secret here
)

// devCAFile is where the CA is exported for clients.
func (o *DevOptions) devCAFile() string {
	return o.HubClusterName + "-ca.crt"
}

// ensureDevCA creates the CA (issued by the railgrid-selfsigned ClusterIssuer)
// and a ClusterIssuer that signs with it, then waits until it is Ready.
// Idempotent: cert-manager keeps the existing CA across re-runs.
func ensureDevCA(ctx context.Context, kubeconfigPath string) error {
	if err := ensureSelfSignedClusterIssuer(ctx, kubeconfigPath); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  isCA: true
  commonName: railgrid local dev CA
  secretName: %[1]s
  duration: 87600h # 10y: re-trusting a new CA every year is pointless for a laptop
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: %[3]s
    kind: ClusterIssuer
    group: cert-manager.io
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %[1]s
spec:
  ca:
    secretName: %[1]s
`, devCAName, devCANamespace, selfSignedClusterIssuerName)
	if err := kubectlApply(ctx, kubeconfigPath, manifest); err != nil {
		return fmt.Errorf("applying the dev CA: %w", err)
	}
	wait := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath,
		"wait", "--for=condition=Ready", "--timeout=120s",
		"-n", devCANamespace, "certificate/"+devCAName)
	if out, err := wait.CombinedOutput(); err != nil {
		return fmt.Errorf("waiting for the dev CA: %w: %s", err, out)
	}
	wait = exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath,
		"wait", "--for=condition=Ready", "--timeout=120s", "clusterissuer/"+devCAName)
	if out, err := wait.CombinedOutput(); err != nil {
		return fmt.Errorf("waiting for the dev CA issuer: %w: %s", err, out)
	}
	return nil
}

// devHubTLSValues makes cert-manager issue the hub's serving certificate from
// the dev CA, for every name clients use to reach it.
func devHubTLSValues() map[string]any {
	return map[string]any{
		"selfSigned": map[string]any{"enabled": false},
		"certManager": map[string]any{
			"enabled": true,
			"issuerRef": map[string]any{
				"name":  devCAName,
				"kind":  "ClusterIssuer",
				"group": "cert-manager.io",
			},
			"dnsNames": []string{
				devHubHost,
				"localhost",
				devHubReleaseName,
				devHubReleaseName + "." + devHubNamespace + ".svc",
				devHubReleaseName + "." + devHubNamespace + ".svc.cluster.local",
			},
		},
	}
}

// devCAPEM reads the dev CA certificate from its Secret.
func devCAPEM(ctx context.Context, clientset kubernetes.Interface) ([]byte, error) {
	secret, err := clientset.CoreV1().Secrets(devCANamespace).Get(ctx, devCAName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading the dev CA: %w", err)
	}
	// A self-signed CA certificate: tls.crt is the CA itself.
	pem := secret.Data["tls.crt"]
	if len(pem) == 0 {
		return nil, fmt.Errorf("dev CA secret %s/%s has no tls.crt", devCANamespace, devCAName)
	}
	return pem, nil
}

// exportDevCA writes the dev CA to devCAFile() and returns the path.
func (o *DevOptions) exportDevCA(ctx context.Context, clientset kubernetes.Interface) (string, error) {
	pem, err := devCAPEM(ctx, clientset)
	if err != nil {
		return "", err
	}
	path := o.devCAFile()
	if err := os.WriteFile(path, pem, 0o644); err != nil { //nolint:gosec // a public CA certificate
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// ensureHubServesDevCert checks that the hub presents a certificate the dev CA
// verifies for devHubHost, and restarts the hub pod when it does not — a hub
// that started before its certificate was (re)issued keeps serving the old
// one, since it reads the Secret only at startup.
func (o *DevOptions) ensureHubServesDevCert(ctx context.Context, clientset kubernetes.Interface) error {
	pem, err := devCAPEM(ctx, clientset)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("dev CA secret holds no PEM certificate")
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(o.HubHTTPSPort))
	verify := func() error { return verifyServedCert(addr, devHubHost, pool) }

	// cert-manager may still be issuing a freshly requested certificate.
	var lastErr error
	if err := pollUntil(ctx, 3*time.Second, 30*time.Second, func(context.Context) (bool, error) {
		lastErr = verify()
		return lastErr == nil, nil
	}, func() error { return lastErr }); err == nil {
		return nil
	}

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Hub serves a certificate the dev CA does not sign (%v); restarting the hub...\n", lastErr)
	pod := devHubReleaseName + "-0"
	if err := clientset.CoreV1().Pods(devHubNamespace).Delete(ctx, pod, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("restarting %s/%s: %w", devHubNamespace, pod, err)
	}
	return pollUntil(ctx, 3*time.Second, o.WaitForReadyTimeout+2*time.Minute, func(context.Context) (bool, error) {
		lastErr = verify()
		return lastErr == nil, nil
	}, func() error { return fmt.Errorf("hub still does not serve a dev-CA certificate: %w", lastErr) })
}

func verifyServedCert(addr, serverName string, roots *x509.CertPool) error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	return conn.Close()
}

// finishHubTLS makes sure the hub serves its dev-CA certificate and exports
// the CA for clients. Runs after every hub install or upgrade.
func (o *DevOptions) finishHubTLS(ctx context.Context, restConfig *rest.Config) error {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	if err := o.ensureHubServesDevCert(ctx, clientset); err != nil {
		return err
	}
	path, err := o.exportDevCA(ctx, clientset)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Dev CA (signs the hub and app certificates) written to %s\n", path)
	return nil
}
