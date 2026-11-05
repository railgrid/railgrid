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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const organizationBootstrapBodyLimit = 8 << 20

// OrganizationBootstrap is the declaration submitted by an organization
// provider. The Hub owns APIExport, cache-slice, endpoint, and RBAC writes.
// Endpoint URLs are intentionally absent from this payload.
type OrganizationBootstrap struct {
	CatalogEntry                  *unstructured.Unstructured   `json:"catalogEntry"`
	APIExport                     *unstructured.Unstructured   `json:"apiExport"`
	Schemas                       []*unstructured.Unstructured `json:"schemas"`
	PrivateCRDs                   []*unstructured.Unstructured `json:"privateCRDs,omitempty"`
	VirtualStorage                []VirtualStorageResource     `json:"virtualStorage,omitempty"`
	APIExportEndpointSliceAliases []string                     `json:"exportEndpointSliceAliases,omitempty"`
}

// VirtualStorageResource asks the Hub to publish one resource from this
// provider workspace through a local ClusterCachedResourceEndpointSlice. The
// SDK sends only the resource coordinate and slice name. Hub admission checks
// that the resource is declared and backed by an admitted private CRD, then
// creates both cache objects itself; no URL or external workspace reference is
// accepted.
type VirtualStorageResource struct {
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Version  string `json:"version,omitempty"`
	Name     string `json:"name"`
}

func bootstrapOrganization(ctx context.Context, opts Options, export *unstructured.Unstructured, workspacePath string) error {
	parts := strings.Split(workspacePath, ":")
	if len(parts) != 6 || parts[0] != "root" || parts[1] != "railgrid" || parts[2] != "tenants" || parts[3] == "" || parts[4] != "providers" || parts[5] == "" {
		return fmt.Errorf("install: expected an organization provider workspace")
	}
	if opts.CatalogEntryFile == "" {
		return fmt.Errorf("install: organization providers require a CatalogEntryFile")
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
	request.APIExportEndpointSliceAliases, err = validatedEndpointSliceAliases(opts.ExportName, opts.APIExportEndpointSliceAliases)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
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
	for resource, reference := range opts.VirtualStorage {
		if reference.APIGroup == nil || *reference.APIGroup != "cache.kcp.io" || reference.Kind != "ClusterCachedResourceEndpointSlice" || reference.Name == "" {
			return fmt.Errorf("install: organization virtual storage must reference a local cache endpoint slice")
		}
		request.VirtualStorage = append(request.VirtualStorage, VirtualStorageResource{
			Group: resource.Group, Resource: resource.Resource, Name: reference.Name,
		})
	}
	sort.Slice(request.VirtualStorage, func(i, j int) bool {
		if request.VirtualStorage[i].Group != request.VirtualStorage[j].Group {
			return request.VirtualStorage[i].Group < request.VirtualStorage[j].Group
		}
		return request.VirtualStorage[i].Resource < request.VirtualStorage[j].Resource
	})
	endpoint, err := url.Parse(opts.Config.Host)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return fmt.Errorf("install: invalid Hub kubeconfig URL")
	}
	clusterPrefix := strings.Index(endpoint.Path, "/clusters/")
	if clusterPrefix < 0 {
		return fmt.Errorf("install: organization kubeconfig must address a Hub workspace")
	}
	endpoint.Path = endpoint.Path[:clusterPrefix] + "/api/providers/" + parts[5] + "/bootstrap"
	endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("install: encoding bootstrap declaration: %w", err)
	}
	if len(body) > organizationBootstrapBodyLimit {
		return fmt.Errorf("install: bootstrap declaration exceeds %d bytes", organizationBootstrapBodyLimit)
	}
	client, err := rest.HTTPClientFor(opts.Config)
	if err != nil {
		return fmt.Errorf("install: bootstrap client: %w", err)
	}
	// Never forward the provider credential if the Hub responds with a redirect.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("install: creating bootstrap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("install: Hub bootstrap: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	detail, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("install: bootstrap response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("install: Hub bootstrap returned %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}
