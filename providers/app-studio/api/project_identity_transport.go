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

package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"k8s.io/client-go/rest"
)

// projectProviderActionTransport uses system roots and the provider kubeconfig's
// existing CA trust. Only the hub's explicit development TLS option can disable
// verification; the provider kubeconfig cannot. Requests use only the Project token.
func projectProviderActionTransport(providerConfig *rest.Config, hubInsecure bool) (http.RoundTripper, error) {
	if providerConfig == nil {
		return nil, errors.New("provider REST config is unavailable")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("cannot enforce verified hub TLS with the configured HTTP transport")
	}
	var bundle []byte
	if len(providerConfig.CAData) != 0 {
		bundle = providerConfig.CAData
	} else if strings.TrimSpace(providerConfig.CAFile) != "" {
		var err error
		bundle, err = os.ReadFile(providerConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read provider kubeconfig CA file %q: %w", providerConfig.CAFile, err)
		}
		if len(bundle) == 0 {
			return nil, errors.New("provider kubeconfig CA bundle contains no PEM certificates")
		}
	}
	return projectProviderActionTransports.load(base, bundle, hubInsecure, func() (*http.Transport, error) {
		transport := base.Clone()
		// These hooks can bypass Transport.TLSClientConfig. Always use the
		// normal TLS handshake governed by the trust policy below.
		transport.DialTLS = nil //nolint:staticcheck // Clear the deprecated hook too; it can bypass certificate verification.
		transport.DialTLSContext = nil
		transport.TLSNextProto = nil
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if len(bundle) != 0 {
			if err := appendHubTrustBundle(roots, bundle, "provider kubeconfig"); err != nil {
				return nil, err
			}
		}
		// The URL host supplies the server name. Build fresh TLS settings,
		// retaining no provider certificate, callback, or credential.
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			RootCAs:            roots,
			InsecureSkipVerify: hubInsecure, // Explicit development opt-in; false by default.
		}
		return transport, nil
	})
}

func appendHubTrustBundle(roots *x509.CertPool, bundle []byte, source string) error {
	if roots == nil || !roots.AppendCertsFromPEM(bundle) {
		return fmt.Errorf("%s CA bundle contains no PEM certificates", source)
	}
	return nil
}
