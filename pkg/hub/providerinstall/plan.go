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

package providerinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/strvals"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

const (
	chartResolutionTimeout = 30 * time.Second
	chartHTTPTimeout       = 30 * time.Second
)

type planResolverConfig struct {
	tempDir   string
	transport http.RoundTripper
}

// ResolvePlan takes only the platform's declared recipe. No shell text, chart
// URLs, Kubernetes objects, or values supplied by a browser are executed.
func ResolvePlan(ctx context.Context, in providers.InstallInstructions) (*Plan, error) {
	return resolvePlan(ctx, in, planResolverConfig{})
}

func resolvePlan(ctx context.Context, in providers.InstallInstructions, resolver planResolverConfig) (*Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, chartResolutionTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("provider chart resolution did not complete: %w", err)
	}
	if len(in.Warnings) > 0 {
		return nil, fmt.Errorf("this provider needs manual installation: %s", strings.Join(in.Warnings, " "))
	}
	p := &Plan{Namespace: in.Namespace, Release: in.ReleaseName, Chart: in.ChartRef, Version: in.ChartVersion, Values: map[string]any{}}
	for _, v := range in.Values {
		if v.Unresolved {
			return nil, fmt.Errorf("set %s using manual installation", v.Name)
		}
		if err := strvals.ParseInto(v.Name+"="+v.Value, p.Values); err != nil {
			return nil, fmt.Errorf("invalid installation value %s", v.Name)
		}
	}
	if err := validateChartReference(p.Chart); err != nil {
		return nil, err
	}
	if p.Version == "" {
		settings := cli.New()
		tempDir, err := os.MkdirTemp(resolver.tempDir, "railgrid-provider-chart-")
		if err != nil {
			return nil, fmt.Errorf("could not prepare provider chart resolution")
		}
		defer func() { _ = os.RemoveAll(tempDir) }()

		// A declared chart URI is self-contained. Give Helm an empty repo config
		// and a task-owned cache so host-local Helm repositories cannot affect it.
		settings.RepositoryConfig = filepath.Join(tempDir, "repositories.yaml")
		settings.RepositoryCache = tempDir

		transport := resolver.transport
		if transport == nil {
			transport = defaultChartTransport()
		}
		registryTransport := &chartContextTransport{ctx: ctx, base: transport}
		httpClient := &http.Client{Transport: registryTransport, Timeout: chartHTTPTimeout}
		registryClient, err := registry.NewClient(registry.ClientOptHTTPClient(httpClient))
		if err != nil {
			return nil, fmt.Errorf("could not resolve the provider chart")
		}
		path, err := locateChartWithContext(ctx, p.Chart, p.Version, settings, registryClient, transport)
		if err != nil {
			return nil, chartResolutionError(ctx, err)
		}
		chart, err := loader.Load(path)
		if err != nil {
			return nil, fmt.Errorf("could not load the provider chart")
		}
		p.Version = chart.Metadata.Version
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("provider chart resolution did not complete: %w", err)
	}
	if _, err := semver.StrictNewVersion(p.Version); err != nil {
		return nil, fmt.Errorf("provider chart version must be an exact semantic version")
	}
	return p, nil
}

func validateChartReference(chartRef string) error {
	if _, err := os.Stat(chartRef); err == nil || filepath.IsAbs(chartRef) || strings.HasPrefix(chartRef, ".") {
		return nil
	}
	parsed, err := url.Parse(chartRef)
	if err == nil && (registry.IsOCI(chartRef) || parsed.Scheme == "http" || parsed.Scheme == "https") {
		if parsed.User != nil {
			return fmt.Errorf("this provider needs manual installation: chart URL credentials are not supported")
		}
		return nil
	}
	return fmt.Errorf("this provider needs manual installation: use a local chart path, OCI URL, or direct HTTP(S) chart URL")
}

func locateChartWithContext(ctx context.Context, chartRef, version string, settings *cli.EnvSettings, registryClient *registry.Client, transport http.RoundTripper) (string, error) {
	if _, err := os.Stat(chartRef); err == nil {
		return filepath.Abs(chartRef)
	}
	if filepath.IsAbs(chartRef) || strings.HasPrefix(chartRef, ".") {
		return chartRef, fmt.Errorf("chart path %q not found", chartRef)
	}
	options := []getter.Option{getter.WithTimeout(chartHTTPTimeout)}
	if registry.IsOCI(chartRef) {
		options = append(options, getter.WithRegistryClient(registryClient))
	}
	dl := downloader.ChartDownloader{
		Out:              io.Discard,
		Getters:          contextHTTPGetters(settings, ctx, transport),
		Options:          options,
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
		RegistryClient:   registryClient,
	}
	filename, _, err := dl.DownloadTo(chartRef, version, settings.RepositoryCache)
	if err != nil {
		return "", err
	}
	return filepath.Abs(filename)
}

func contextHTTPGetters(settings *cli.EnvSettings, ctx context.Context, transport http.RoundTripper) getter.Providers {
	providers := getter.All(settings)
	client := &http.Client{Transport: transport, Timeout: chartHTTPTimeout}
	for i := range providers {
		for _, scheme := range providers[i].Schemes {
			if scheme == "http" || scheme == "https" {
				providers[i].New = func(...getter.Option) (getter.Getter, error) {
					return &contextHTTPGetter{ctx: ctx, client: client}, nil
				}
				break
			}
		}
	}
	return providers
}

type contextHTTPGetter struct {
	ctx    context.Context
	client *http.Client
}

func (g *contextHTTPGetter) Get(href string, _ ...getter.Option) (*bytes.Buffer, error) {
	request, err := http.NewRequestWithContext(g.ctx, http.MethodGet, href, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/gzip,application/octet-stream")
	request.Header.Set("User-Agent", "Railgrid-ProviderInstaller")
	response, err := g.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch chart: %s", response.Status)
	}
	body := bytes.NewBuffer(nil)
	if _, err := io.Copy(body, response.Body); err != nil {
		return nil, err
	}
	return body, nil
}

func chartResolutionError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("provider chart resolution did not complete: %w", ctxErr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("provider chart resolution did not complete: %w", err)
	}
	return fmt.Errorf("could not resolve the provider chart")
}

func defaultChartTransport() http.RoundTripper {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return http.DefaultTransport
}

type chartContextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t *chartContextTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request.WithContext(t.ctx))
}
