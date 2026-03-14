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

package cmd

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

var (
	kubeconfig        string
	globalInsecureTLS bool
)

func loadRestConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			configOverrides := &clientcmd.ConfigOverrides{}
			kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
			config, err = kubeConfig.ClientConfig()
		}
	}
	if err != nil {
		return nil, err
	}
	if globalInsecureTLS {
		config.Insecure = true
		config.CAData = nil
		config.CAFile = ""
	}
	return config, nil
}

func loadDynamicClient() (dynamic.Interface, error) {
	config, err := loadRestConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	return dynamic.NewForConfig(config)
}

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func formatStringOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printRow(tw *tabwriter.Writer, cols ...string) {
	_, _ = fmt.Fprintln(tw, strings.Join(cols, "\t"))
}

// externalizeEdgeURLFromConfig replaces the host in an edge URL with the hub's
// external address from a rest.Config. edge.Status.URL may use an internal host
// (e.g. localhost) for kcp mount resolution; this function swaps in the hub's
// public address so the URL is accessible from the user's machine.
func externalizeEdgeURLFromConfig(edgeURL string, config *rest.Config) (string, error) {
	parsed, err := url.Parse(edgeURL)
	if err != nil {
		return "", fmt.Errorf("parsing edge URL %q: %w", edgeURL, err)
	}

	hubParsed, err := url.Parse(config.Host)
	if err != nil {
		return edgeURL, nil //nolint:nilerr // can't parse hub host, return as-is
	}

	// Only externalize if the path looks like an edges-proxy path.
	if !strings.HasPrefix(parsed.Path, "/services/") {
		return edgeURL, nil
	}

	hubBase := hubParsed.Scheme + "://" + hubParsed.Host
	return hubBase + parsed.Path, nil
}

// externalizeEdgeURL replaces the host in an edge URL with the hub's external
// address from a raw kubeconfig. See externalizeEdgeURLFromConfig.
func externalizeEdgeURL(edgeURL string, rawConfig *clientcmdapi.Config) (string, error) {
	hubServerURL := ""
	if currentCtx, ok := rawConfig.Contexts[rawConfig.CurrentContext]; ok {
		if cl, ok := rawConfig.Clusters[currentCtx.Cluster]; ok {
			hubServerURL = cl.Server
		}
	}
	if hubServerURL == "" {
		return edgeURL, nil
	}

	parsed, err := url.Parse(edgeURL)
	if err != nil {
		return "", fmt.Errorf("parsing edge URL %q: %w", edgeURL, err)
	}

	hubParsed, err := url.Parse(hubServerURL)
	if err != nil {
		return edgeURL, nil //nolint:nilerr
	}

	if !strings.HasPrefix(parsed.Path, "/services/") {
		return edgeURL, nil
	}

	hubBase := hubParsed.Scheme + "://" + hubParsed.Host
	return hubBase + parsed.Path, nil
}
