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

package hubclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// EnvHubCAFile is a PEM bundle (path) that verifies the hub's serving
	// certificate. It is the same variable the edges chart already sets from
	// hub.caSecretRef.
	EnvHubCAFile = "RAILGRID_HUB_CA_FILE"
	// EnvHubCAData is the same bundle inline (hub.caData in the edges chart).
	EnvHubCAData = "RAILGRID_HUB_CA_DATA"
)

// HubCA returns the PEM bundle a provider verifies the hub's serving
// certificate with, in order:
//
//  1. RAILGRID_HUB_CA_FILE, a path;
//  2. RAILGRID_HUB_CA_DATA, inline PEM;
//  3. the certificate authority of the kubeconfig at
//     RAILGRID_PROVIDER_KUBECONFIG. The hub mints that kubeconfig with the
//     bundle from its providerCASecretName, which covers the hub's own
//     serving certificate, so a provider that can reach kcp through it can
//     verify the hub with it too.
//
// A nil bundle with a nil error means nothing is configured and hub calls
// verify against the system roots alone; that is the right answer for a hub
// behind a publicly trusted certificate. A source that is set but cannot be
// read is an error, so a broken mount surfaces instead of silently falling
// back to a trust store that will reject the hub.
func HubCA() ([]byte, error) {
	if p := strings.TrimSpace(os.Getenv(EnvHubCAFile)); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading %s=%s: %w", EnvHubCAFile, p, err)
		}
		return b, nil
	}
	if d := strings.TrimSpace(os.Getenv(EnvHubCAData)); d != "" {
		return []byte(d), nil
	}
	if p := strings.TrimSpace(os.Getenv(EnvProviderKubeconfig)); p != "" {
		return CAFromKubeconfig(p)
	}
	return nil, nil
}

// CAFromKubeconfig returns the certificate authority bundle the kubeconfig at
// path verifies its server with (inline certificate-authority-data, or the
// file certificate-authority names). nil when the kubeconfig carries none,
// which is the case for a kubeconfig minted by a hub in development mode.
func CAFromKubeconfig(path string) ([]byte, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s=%s: %w", EnvProviderKubeconfig, path, err)
	}
	rc, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving %s=%s: %w", EnvProviderKubeconfig, path, err)
	}
	if len(rc.CAData) > 0 {
		return rc.CAData, nil
	}
	if rc.CAFile != "" {
		b, err := os.ReadFile(rc.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading certificate-authority %s of %s=%s: %w", rc.CAFile, EnvProviderKubeconfig, path, err)
		}
		return b, nil
	}
	return nil, nil
}

// HubTLSConfig returns the TLS client configuration for an HTTP client that
// talks to the hub. With insecure it skips verification (RAILGRID_HUB_INSECURE,
// development only). Otherwise it verifies against the system roots plus
// HubCA, so one configuration serves a hub behind a public certificate and a
// hub serving a private one on its in-cluster Service name.
func HubTLSConfig(insecure bool) (*tls.Config, error) {
	if insecure {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // dev-only; opt-in via RAILGRID_HUB_INSECURE
	}
	ca, err := HubCA()
	if err != nil {
		return nil, err
	}
	if len(ca) == 0 {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("hub CA bundle contains no PEM certificate")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil
}

// HubTransport returns an http.Transport for hub calls: a clone of
// http.DefaultTransport carrying HubTLSConfig. Each call builds a fresh
// transport; share it where connection reuse matters.
func HubTransport(insecure bool) (*http.Transport, error) {
	tlsConfig, err := HubTLSConfig(insecure)
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = tlsConfig
	return t, nil
}

var (
	sharedMu        sync.Mutex
	sharedTransport = map[bool]*http.Transport{}
)

// SharedHubTransport is HubTransport memoised per process and per insecure
// flag, for the rest.Configs a provider builds per request or per workspace:
// client-go caches transports by TLS configuration, and this keeps one
// connection pool across those configs the same way.
func SharedHubTransport(insecure bool) (*http.Transport, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if t, ok := sharedTransport[insecure]; ok {
		return t, nil
	}
	t, err := HubTransport(insecure)
	if err != nil {
		return nil, err
	}
	sharedTransport[insecure] = t
	return t, nil
}

// ApplyHubTLS makes cfg verify the hub the way HubTLSConfig does. A config
// that already names a CA, a client certificate or Insecure is left alone:
// the caller decided its trust explicitly. Otherwise the config's transport
// becomes SharedHubTransport, which client-go still wraps with the config's
// bearer token and other HTTP-level settings.
func ApplyHubTLS(cfg *rest.Config, insecure bool) error {
	tc := cfg.TLSClientConfig
	if cfg.Transport != nil || tc.Insecure || len(tc.CAData) > 0 || tc.CAFile != "" || len(tc.CertData) > 0 || tc.CertFile != "" {
		return nil
	}
	if insecure {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
		return nil
	}
	ca, err := HubCA()
	if err != nil {
		return err
	}
	if len(ca) == 0 {
		return nil
	}
	t, err := SharedHubTransport(false)
	if err != nil {
		return err
	}
	cfg.Transport = t
	return nil
}
