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

package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

// OrganizationBootstrap is the declaration submitted by an org-owned provider.
// The hub owns routing and RBAC. No endpoint URL or privilege is accepted merely
// because it appears in this payload.
type OrganizationBootstrap struct {
	CatalogEntry *unstructured.Unstructured   `json:"catalogEntry"`
	APIExport    *unstructured.Unstructured   `json:"apiExport"`
	Schemas      []*unstructured.Unstructured `json:"schemas"`
	PrivateCRDs  []*unstructured.Unstructured `json:"privateCRDs,omitempty"`
}

func bootstrapOrganization(ctx context.Context, opts Options, export *unstructured.Unstructured, workspacePath string) error {
	parts := strings.Split(workspacePath, ":")
	if len(parts) != 6 || parts[0] != "root" || parts[1] != "railgrid" || parts[2] != "tenants" || parts[3] == "" || parts[4] != "providers" || parts[5] == "" {
		return fmt.Errorf("install: expected an organization provider workspace")
	}
	if opts.CatalogEntryFile == "" {
		return fmt.Errorf("install: org providers require a CatalogEntryFile")
	}
	if len(opts.VirtualStorage) != 0 {
		return fmt.Errorf("install: org providers cannot supply virtual storage endpoints")
	}
	raw, err := os.ReadFile(opts.CatalogEntryFile)
	if err != nil {
		return fmt.Errorf("install: reading CatalogEntry: %w", err)
	}
	entry := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, &entry.Object); err != nil {
		return fmt.Errorf("install: parsing CatalogEntry: %w", err)
	}
	request := OrganizationBootstrap{CatalogEntry: entry, APIExport: export, PrivateCRDs: opts.PrivateCRDs}
	files, err := schemaFiles(opts.KCPDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		object, err := readSchema(file)
		if err != nil {
			return err
		}
		request.Schemas = append(request.Schemas, object)
	}
	endpoint, err := url.Parse(opts.Config.Host)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return fmt.Errorf("install: invalid hub kubeconfig URL")
	}
	clusterPrefix := strings.Index(endpoint.Path, "/clusters/")
	if clusterPrefix < 0 {
		return fmt.Errorf("install: org kubeconfig must address a hub workspace")
	}
	endpoint.Path = endpoint.Path[:clusterPrefix] + "/api/providers/" + parts[5] + "/bootstrap"
	endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(body) > 8<<20 {
		return fmt.Errorf("install: bootstrap declaration exceeds 8 MiB")
	}
	client, err := rest.HTTPClientFor(opts.Config)
	if err != nil {
		return fmt.Errorf("install: bootstrap client: %w", err)
	}
	// A redirect must never move the provider credential to another endpoint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("install: hub bootstrap: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	detail, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("install: bootstrap response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("install: hub bootstrap returned %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}
