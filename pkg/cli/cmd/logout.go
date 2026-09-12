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
	"sort"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"

	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

func newLogoutCommand() *cobra.Command {
	var keepContexts bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the hub credentials on this machine",
		Long: `Delete the cached OIDC tokens for the hub and remove the faros kubeconfig
context (and every faros-<edge> context created by 'faros connect'). The
hub-side session is untouched; log in again with 'faros login'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, path, err := loadRawKubeconfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			kctx, ok := raw.Contexts[farosContextName]
			if !ok {
				_, _ = fmt.Fprintf(out, "Not logged in: no %q context in %s.\n", farosContextName, path)
				return nil
			}

			// 1. Token cache, keyed by the issuer/client the exec plugin uses.
			if auth := raw.AuthInfos[kctx.AuthInfo]; auth != nil && auth.Exec != nil {
				issuer, clientID := execOIDCArgs(auth.Exec)
				if issuer != "" && clientID != "" {
					removed, err := cliauth.RemoveTokenCache(issuer, clientID)
					if err != nil {
						return fmt.Errorf("removing token cache: %w", err)
					}
					if removed {
						_, _ = fmt.Fprintln(out, "Removed cached OIDC tokens.")
					}
				}
			}

			// 2. Kubeconfig entries.
			removedCtx := []string{farosContextName}
			userName, clusterName := kctx.AuthInfo, kctx.Cluster
			delete(raw.Contexts, farosContextName)
			if !keepContexts {
				for name, c := range raw.Contexts {
					if isEdgeContext(name) && c.AuthInfo == userName {
						delete(raw.Contexts, name)
						delete(raw.Clusters, c.Cluster)
						removedCtx = append(removedCtx, name)
					}
				}
			}
			delete(raw.Clusters, clusterName)
			delete(raw.AuthInfos, userName)
			for _, name := range removedCtx {
				if raw.CurrentContext == name {
					raw.CurrentContext = ""
				}
			}
			if err := clientcmd.WriteToFile(*raw, path); err != nil {
				return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
			}
			sort.Strings(removedCtx)
			_, _ = fmt.Fprintf(out, "Removed kubeconfig context(s) %v from %s.\nLogged out. Run 'faros login --hub-url <hub>' to log in again.\n", removedCtx, path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepContexts, "keep-edge-contexts", false, "Keep the faros-<edge> contexts (they stop working until you log in again)")
	return cmd
}
