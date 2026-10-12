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

package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func newAppIdentityKeyRotationCommand() *cobra.Command {
	var kubeconfigPath string
	var contextName string

	command := &cobra.Command{
		Use:   "rotate-app-identity-key",
		Short: "Rotate the Railgrid Auth app identity signing key",
		Long: "Rotate the durable signing key used for Railgrid Auth app identity assertions. " +
			"The previous public key remains available for 15 minutes so existing assertions can be verified; " +
			"the stable subject-pseudonym key is preserved. The keyring Secret must already exist; " +
			"this command does not initialize it. This command uses only the supplied kubeconfig " +
			"and context, scopes its client to root:railgrid:system:controllers, and relies on kcp RBAC for authorization.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := appIdentityKeyRotationRESTConfig(kubeconfigPath, contextName)
			if err != nil {
				return err
			}
			keyRing, err := serviceaccounts.NewKCPAppIdentityKeyRing(config, kcp.HubSystemNamespace)
			if err != nil {
				return fmt.Errorf("building Railgrid Auth key ring: %w", err)
			}
			if err := keyRing.RotateAppIdentitySigningKey(command.Context()); err != nil {
				return fmt.Errorf("rotating Railgrid Auth app identity signing key: %w", err)
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), "Rotated the Railgrid Auth app identity signing key.")
			return err
		},
	}
	command.Flags().StringVar(&kubeconfigPath, "kubeconfig", "", "Explicit kubeconfig for kcp (required; no in-cluster or ambient fallback)")
	command.Flags().StringVar(&contextName, "context", "", "Kubeconfig context to use (defaults to that file's current context)")
	_ = command.MarkFlagRequired("kubeconfig")
	return command
}

func appIdentityKeyRotationRESTConfig(kubeconfigPath, contextName string) (*rest.Config, error) {
	if strings.TrimSpace(kubeconfigPath) == "" {
		return nil, fmt.Errorf("--kubeconfig is required; no ambient credentials are used")
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfigPath
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kcp kubeconfig: %w", err)
	}
	config = rest.CopyConfig(config)
	config.Host = apiurl.KCPClusterURL(config.Host, kcppaths.SystemControllers)
	parsedURL, err := url.Parse(config.Host)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil ||
		parsedURL.RawQuery != "" || parsedURL.ForceQuery || parsedURL.Fragment != "" {
		return nil, fmt.Errorf("kcp kubeconfig has an invalid server URL")
	}
	_, clusterPath := apiurl.SplitBaseAndCluster(config.Host)
	if clusterPath != kcppaths.SystemControllers || parsedURL.Path != "/clusters/"+kcppaths.SystemControllers {
		return nil, fmt.Errorf("refusing to rotate outside kcp workspace %q", kcppaths.SystemControllers)
	}
	return config, nil
}
