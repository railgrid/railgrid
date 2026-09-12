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
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func newOrgCommand() *cobra.Command {
	var target hubTarget
	cmd := &cobra.Command{
		Use:     "org",
		Aliases: []string{"orgs", "organization", "organizations"},
		Short:   "Organizations you belong to, and who is in them",
		Long: `An organization owns workspaces and members. You get a personal
organization on first login; teams create shared ones.

  faros org list                      # your organizations and your role in each
  faros org members                   # members of the current organization
  faros org members --org acme        # …of another one you belong to
  faros org create "Acme"`,
	}
	cmd.PersistentFlags().StringVar(&target.org, "org", "", "Organization display name or UUID (default: the one owning the current workspace)")
	cmd.AddCommand(newOrgListCommand(), newOrgCreateCommand(), newMembersCommand(orgScope, &target))
	return cmd
}

func newOrgListCommand() *cobra.Command {
	output := newOutputFlags()
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the organizations you belong to",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := openHubSession()
			if err != nil {
				return err
			}
			orgs, err := fetchOrgs(ctx, s.client, s.Hub)
			if err != nil {
				return withLoginHint(err)
			}
			// Mark the org that owns the kubeconfig's workspace, best effort.
			current := ""
			if err := s.resolveTenant(ctx, hubTarget{}); err == nil {
				current = s.Org.UUID
			}
			return printOrgs(cmd.OutOrStdout(), output, orgs, current)
		},
	}
	output.addFlag(cmd)
	return cmd
}

func printOrgs(w io.Writer, output *outputFlags, orgs []orgView, current string) error {
	switch {
	case output.structured():
		return output.printStructured(w, orgs)
	case output.names():
		names := make([]string, len(orgs))
		for i, o := range orgs {
			names[i] = displayLabel(o.DisplayName, o.UUID)
		}
		return printNames(w, names)
	}
	if len(orgs) == 0 {
		_, err := fmt.Fprintln(w, "You are not a member of any organization.")
		return err
	}
	t := &table{headers: []string{"CURRENT", "NAME", "ROLE", "KIND", "UUID"}}
	if output.wide() {
		t.headers = append(t.headers, "WORKSPACE CREATION", "CATALOG ENTRIES", "AGE")
	}
	for _, o := range orgs {
		mark := ""
		if o.UUID == current {
			mark = "*"
		}
		kind := "team"
		if o.Personal {
			kind = "personal"
		}
		cols := []string{mark, displayLabel(o.DisplayName, o.UUID), o.Role, kind, o.UUID}
		if output.wide() {
			age := ""
			if !o.CreatedAt.IsZero() {
				age = formatAge(o.CreatedAt)
			}
			cols = append(cols, o.WorkspaceCreation, o.CatalogEntryCreation, age)
		}
		t.add(cols...)
	}
	return t.write(w)
}

func newOrgCreateCommand() *cobra.Command {
	var workspaceCreation, catalogEntryCreation string
	cmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create an organization (you become its admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := openHubSession()
			if err != nil {
				return err
			}
			body := map[string]string{"displayName": args[0]}
			if workspaceCreation != "" {
				body["workspaceCreation"] = workspaceCreation
			}
			if catalogEntryCreation != "" {
				body["catalogEntryCreation"] = catalogEntryCreation
			}
			var created orgView
			if err := s.do(ctx, http.MethodPost, s.Hub+"/api/orgs", body, &created); err != nil {
				return fmt.Errorf("creating organization: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Organization %q created (%s).\nSwitch to it with: faros use --org %s\n", created.DisplayName, created.UUID, created.UUID)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspaceCreation, "workspace-creation", "", "Who may create workspaces: members (default) or admin")
	cmd.Flags().StringVar(&catalogEntryCreation, "catalog-entry-creation", "", "Who may publish catalog entries: members or admin (default)")
	return cmd
}

// fetchOrgsSession lists orgs with the session's client (a thin wrapper so
// callers with a session do not need the raw http.Client).
func (s *hubSession) fetchOrgs(ctx context.Context) ([]orgView, error) {
	orgs, err := fetchOrgs(ctx, s.client, s.Hub)
	if err != nil {
		return nil, withLoginHint(err)
	}
	return orgs, nil
}
