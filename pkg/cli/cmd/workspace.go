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
	"net/http"

	"github.com/spf13/cobra"
)

func newWorkspaceCommand() *cobra.Command {
	var target hubTarget
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"workspaces", "ws"},
		Short:   "Workspaces of an organization, and who is in them",
		Long: `A workspace is the Kubernetes-style API you work in: edges, providers and
their resources live there, and access is per workspace.

  faros workspace list                      # workspaces in the current org
  faros workspace list --org acme
  faros workspace members                   # members of the current workspace
  faros workspace members --workspace platform
  faros workspace create "Platform"
  faros use --workspace platform            # make it the kubectl target`,
	}
	target.addFlags(cmd)
	cmd.AddCommand(newWorkspaceListCommand(&target), newWorkspaceCreateCommand(&target), newMembersCommand(workspaceScope, &target))
	return cmd
}

func newWorkspaceListCommand(target *hubTarget) *cobra.Command {
	output := newOutputFlags()
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the workspaces of an organization",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := openHubSession()
			if err != nil {
				return err
			}
			// Remember which workspace kubectl points at before resolveOrg
			// may retarget the session.
			current := s.Cluster
			if err := s.resolveOrg(ctx, target.org); err != nil {
				return err
			}
			workspaces, err := fetchWorkspaces(ctx, s.client, s.Hub, s.Org.UUID)
			if err != nil {
				return withLoginHint(err)
			}
			return printWorkspaces(cmd.OutOrStdout(), output, s.Org, workspaces, current)
		},
	}
	output.addFlag(cmd)
	return cmd
}

func printWorkspaces(w io.Writer, output *outputFlags, org orgView, workspaces []workspaceView, currentCluster string) error {
	switch {
	case output.structured():
		return output.printStructured(w, workspaces)
	case output.names():
		names := make([]string, len(workspaces))
		for i, ws := range workspaces {
			names[i] = displayLabel(ws.DisplayName, ws.UUID)
		}
		return printNames(w, names)
	}
	if len(workspaces) == 0 {
		_, err := fmt.Fprintf(w, "Organization %q has no workspaces you can access.\n", displayLabel(org.DisplayName, org.UUID))
		return err
	}
	t := &table{headers: []string{"CURRENT", "NAME", "ROLE", "UUID", "CLUSTER"}}
	for _, ws := range workspaces {
		mark := ""
		if ws.ClusterName != "" && ws.ClusterName == currentCluster {
			mark = "*"
		}
		cluster := ws.ClusterName
		if cluster == "" {
			cluster = "(not ready)"
		}
		t.add(mark, displayLabel(ws.DisplayName, ws.UUID), ws.Role, ws.UUID, cluster)
	}
	return t.write(w)
}

func newWorkspaceCreateCommand(target *hubTarget) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create a workspace in an organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := openHubSession()
			if err != nil {
				return err
			}
			if err := s.resolveOrg(ctx, target.org); err != nil {
				return err
			}
			var created workspaceView
			if err := s.orgScoped().do(ctx, http.MethodPost, s.Hub+"/api/orgs/"+s.Org.UUID+"/workspaces", map[string]string{"displayName": args[0]}, &created); err != nil {
				return fmt.Errorf("creating workspace: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Workspace %q created (%s) in organization %q.\nSwitch to it with: faros use --org %s --workspace %s\n",
				displayLabel(created.DisplayName, created.UUID), created.UUID, displayLabel(s.Org.DisplayName, s.Org.UUID), s.Org.UUID, created.UUID)
			return nil
		},
	}
	return cmd
}
