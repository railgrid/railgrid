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

package api

import (
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

const actionsCABundleMaxBytes = 1 << 20

// loadActionsCABundleFromEnv reads the explicitly configured public trust
// material for action-enabled development runtimes. A file is preferred so a
// PEM bundle does not become part of process arguments or a deployment value;
// KEDGE_ACTIONS_CA_BUNDLE is a small direct-value escape hatch for local
// launches. If both forms are supplied they must contain the same normalized
// bytes, avoiding an ambiguous source of trust.
//
// An unset pair is valid and returns an empty bundle. Callers defer any
// configuration error until a project actually has an active action grant, so
// an unrelated/actionless project remains usable.
func loadActionsCABundleFromEnv() (string, error) {
	filePath := strings.TrimSpace(os.Getenv("KEDGE_ACTIONS_CA_BUNDLE_FILE"))
	direct := strings.TrimSpace(os.Getenv("KEDGE_ACTIONS_CA_BUNDLE"))
	if filePath == "" && direct == "" {
		return "", nil
	}

	fromFile := ""
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read KEDGE_ACTIONS_CA_BUNDLE_FILE %q: %w", filePath, err)
		}
		if len(data) > actionsCABundleMaxBytes {
			return "", fmt.Errorf("KEDGE_ACTIONS_CA_BUNDLE_FILE %q exceeds %d bytes", filePath, actionsCABundleMaxBytes)
		}
		fromFile = string(data)
	}

	if fromFile != "" && direct != "" && normalizeCABundle(fromFile) != normalizeCABundle(direct) {
		return "", fmt.Errorf("KEDGE_ACTIONS_CA_BUNDLE_FILE and KEDGE_ACTIONS_CA_BUNDLE disagree")
	}
	bundle := fromFile
	if bundle == "" {
		bundle = direct
	}
	bundle = normalizeCABundle(bundle)
	if bundle == "" {
		return "", fmt.Errorf("configured actions CA bundle is empty")
	}
	if len(bundle) > actionsCABundleMaxBytes {
		return "", fmt.Errorf("configured actions CA bundle exceeds %d bytes", actionsCABundleMaxBytes)
	}
	if err := validateCABundle(bundle); err != nil {
		return "", err
	}
	return bundle, nil
}

func normalizeCABundle(raw string) string {
	return strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
}

func validateCABundle(raw string) error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(raw)) {
		return fmt.Errorf("configured actions CA bundle contains no PEM certificates")
	}
	return nil
}
