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

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

// renderHelm fetches the chart archive hub-side and templates it, returning the
// rendered objects (hooks and CRDs excluded). fullnameOverride is forced to the
// Workload name so the chart's Service name is deterministic — the marketplace
// wires an edges Service targetRef to "<workload>.<ns>.svc".
func renderHelm(ctx context.Context, vw *edgesv1alpha1.Workload) ([]*unstructured.Unstructured, error) {
	h := vw.Spec.Helm
	ch, err := fetchChart(ctx, h.RepoURL, h.Chart, h.Version)
	if err != nil {
		return nil, fmt.Errorf("fetching chart %s-%s: %w", h.Chart, h.Version, err)
	}

	vals := map[string]any{}
	if h.Values != nil && len(h.Values.Raw) > 0 {
		if err := json.Unmarshal(h.Values.Raw, &vals); err != nil {
			return nil, fmt.Errorf("decoding helm values: %w", err)
		}
	}
	// Deterministic resource names so targetRef wiring is predictable. Charts
	// vary in which key they honour, so set the common ones.
	vals["fullnameOverride"] = vw.Name
	vals["nameOverride"] = vw.Name

	regClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("helm registry client: %w", err)
	}
	cfg := &action.Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities:   chartutil.DefaultCapabilities,
		RegistryClient: regClient,
		Log:            func(string, ...any) {},
	}

	inst := action.NewInstall(cfg)
	inst.ReleaseName = vw.Name
	inst.Namespace = targetNamespace
	inst.DryRun = true
	inst.ClientOnly = true // no cluster access: pure template
	inst.IncludeCRDs = false
	inst.DisableHooks = true

	rel, err := inst.Run(ch, vals)
	if err != nil {
		return nil, fmt.Errorf("helm template: %w", err)
	}
	return splitManifests(rel.Manifest)
}

// fetchChart downloads a chart archive from "<repoURL>/<name>-<version>.tgz".
// Classic http chart repos (grafana, prometheus-community, portainer, …) serve
// this predictable path, which lets us skip repo-index machinery.
func fetchChart(ctx context.Context, repoURL, name, version string) (*chart.Chart, error) {
	url := strings.TrimRight(repoURL, "/") + "/" + name + "-" + version + ".tgz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	// Guard against a runaway download (charts are small; cap at 50MiB).
	return loader.LoadArchive(io.LimitReader(resp.Body, 50<<20))
}

// splitManifests parses helm's rendered multi-document YAML into objects,
// dropping empty documents and any doc without a kind.
func splitManifests(manifest string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, doc := range strings.Split(manifest, "\n---") {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		m := map[string]any{}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, fmt.Errorf("parsing rendered manifest: %w", err)
		}
		u := &unstructured.Unstructured{Object: m}
		if u.GetKind() == "" || u.GetAPIVersion() == "" {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}
