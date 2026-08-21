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

package telemetry

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// NewHTTPClientWithCAFile returns a dedicated HTTP client for the telemetry
// receiver. An empty path returns nil so callers retain the normal
// http.DefaultTransport behavior for public-CA endpoints. When a path is
// supplied, the system roots are retained and the PEM certificates in the
// file are appended to a clone of the default transport's TLS configuration.
//
// The returned client is intentionally separate from shared/default clients:
// trusting a private telemetry CA must not change trust for hub, provider, or
// identity-provider traffic.
func NewHTTPClientWithCAFile(path string) (*http.Client, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read telemetry CA file %q: %w", path, err)
	}

	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate roots for telemetry: %w", err)
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("telemetry CA file %q contains no valid PEM certificates", path)
	}

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("clone default HTTP transport for telemetry: unexpected type %T", http.DefaultTransport)
	}
	transport := baseTransport.Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.RootCAs = roots
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport}, nil
}
