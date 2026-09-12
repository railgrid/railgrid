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
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

var (
	kubeconfig        string
	globalInsecureTLS bool
)

// normalizeHubURL ensures the URL has a scheme. If no scheme is present,
// https:// is prepended. This allows users to type just "hub.faros.sh" instead
// of "https://hub.faros.sh".
func normalizeHubURL(u string) string {
	if u == "" {
		return u
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "https://" + u
	}
	return u
}

// loadRestConfig returns the client config for talking to the hub. It uses
// the "faros" context whenever the kubeconfig has one, so hub commands keep
// working while 'faros connect' has pointed kubectl's current context at an
// edge; without one it falls back to the current context (or in-cluster).
func loadRestConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if raw, rerr := loadingRules.Load(); rerr == nil {
		if _, ok := raw.Contexts[farosContextName]; ok {
			overrides.CurrentContext = farosContextName
		}
	}
	if kubeconfig != "" || overrides.CurrentContext != "" {
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
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

	// status.URL may be a full URL with an internal host, or a bare path when
	// the provider has no public host configured; both become hub-relative.
	if !strings.HasPrefix(parsed.Path, "/services/") {
		return edgeURL, nil
	}

	hubBase := hubParsed.Scheme + "://" + hubParsed.Host
	return hubBase + parsed.Path, nil
}

// stdinIsTerminal reports whether the process has an interactive stdin.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// confirm asks a yes/no question on the command's streams. Without a
// terminal it refuses rather than guessing, so scripts must pass --yes.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if !stdinIsTerminal() {
		return false, fmt.Errorf("%s\nconfirmation needed but stdin is not a terminal; pass --yes", question)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// loadRawKubeconfig loads the kubeconfig file the CLI operates on (the
// --kubeconfig path, else the default loading rules) and the path writes
// must go to.
func loadRawKubeconfig() (*clientcmdapi.Config, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	raw, err := loadingRules.GetStartingConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	path := loadingRules.GetDefaultFilename()
	if kubeconfig != "" {
		path = kubeconfig
	}
	return raw, path, nil
}
