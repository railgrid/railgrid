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
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Membership roles the hub accepts, at both the org and the workspace scope.
const (
	roleAdmin  = "admin"
	roleMember = "member"
)

// memberView mirrors the hub's MembershipView.
type memberView struct {
	User                 string `json:"user"`
	RBACIdentity         string `json:"rbacIdentity,omitempty"`
	Email                string `json:"email,omitempty"`
	UserDisplayName      string `json:"userDisplayName,omitempty"`
	Role                 string `json:"role"`
	OrgUUID              string `json:"orgUUID,omitempty"`
	WorkspaceUUID        string `json:"workspaceUUID,omitempty"`
	OrgDisplayName       string `json:"orgDisplayName,omitempty"`
	WorkspaceDisplayName string `json:"workspaceDisplayName,omitempty"`
}

// label is how a member is shown: email, then display name, then the User
// name (an opaque CR name) as a last resort.
func (m memberView) label() string {
	switch {
	case m.Email != "":
		return m.Email
	case m.UserDisplayName != "":
		return m.UserDisplayName
	default:
		return m.User
	}
}

// membershipScope selects the org-level or workspace-level membership API.
// Both scopes share one wire format and one set of verbs, so the commands
// are generated from this description.
type membershipScope struct {
	// noun is "organization" or "workspace"; used in messages.
	noun string
	// workspace reports whether the workspace-scope routes are used.
	workspace bool
}

var (
	orgScope       = membershipScope{noun: "organization"}
	workspaceScope = membershipScope{noun: "workspace", workspace: true}
)

// session resolves the tenant for the scope: org-only for org routes,
// org+workspace for workspace routes.
func (sc membershipScope) session(ctx context.Context, target hubTarget) (*hubSession, error) {
	if sc.workspace {
		return newHubSession(ctx, target)
	}
	s, err := openHubSession()
	if err != nil {
		return nil, err
	}
	if err := s.resolveOrg(ctx, target.org); err != nil {
		return nil, err
	}
	return s.orgScoped(), nil
}

func (sc membershipScope) url(s *hubSession) string {
	if sc.workspace {
		return fmt.Sprintf("%s/api/orgs/%s/workspaces/%s/memberships", s.Hub, s.Org.UUID, s.WS.UUID)
	}
	return fmt.Sprintf("%s/api/orgs/%s/memberships", s.Hub, s.Org.UUID)
}

// where names the resolved target for messages: 'organization "Acme"' or
// 'workspace "platform" (organization "Acme")'.
func (sc membershipScope) where(s *hubSession) string {
	org := fmt.Sprintf("organization %q", displayLabel(s.Org.DisplayName, s.Org.UUID))
	if !sc.workspace {
		return org
	}
	return fmt.Sprintf("workspace %q (%s)", displayLabel(s.WS.DisplayName, s.WS.UUID), org)
}

func (sc membershipScope) list(ctx context.Context, s *hubSession) ([]memberView, error) {
	var resp listResponse[memberView]
	if err := s.do(ctx, http.MethodGet, sc.url(s), nil, &resp); err != nil {
		return nil, fmt.Errorf("listing %s members: %w", sc.noun, err)
	}
	sort.SliceStable(resp.Items, func(i, j int) bool {
		if resp.Items[i].Role != resp.Items[j].Role {
			return resp.Items[i].Role == roleAdmin
		}
		return strings.ToLower(resp.Items[i].label()) < strings.ToLower(resp.Items[j].label())
	})
	return resp.Items, nil
}

// resolveMember finds one member by User name, email, RBAC identity or
// display name (case-insensitive), so users can type what they see.
func resolveMember(members []memberView, query string) (memberView, error) {
	q := strings.TrimSpace(query)
	var hits []memberView
	for _, m := range members {
		if m.User == q {
			return m, nil
		}
		if strings.EqualFold(m.Email, q) || strings.EqualFold(m.RBACIdentity, q) ||
			strings.EqualFold(strings.TrimPrefix(m.RBACIdentity, "faros:"), q) ||
			(m.UserDisplayName != "" && strings.EqualFold(m.UserDisplayName, q)) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return memberView{}, fmt.Errorf("no member matches %q", query)
	default:
		ids := make([]string, len(hits))
		for i, m := range hits {
			ids[i] = m.User
		}
		return memberView{}, fmt.Errorf("%q matches %d members; use a user id: %s", query, len(hits), strings.Join(ids, ", "))
	}
}

func validateRole(role string) error {
	switch role {
	case roleAdmin, roleMember:
		return nil
	}
	return fmt.Errorf("invalid role %q (want %s or %s)", role, roleAdmin, roleMember)
}

func completeRoles(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return []string{roleAdmin, roleMember}, cobra.ShellCompDirectiveNoFileComp
}

// newMembersCommand builds the 'members' subtree for a scope: list (the
// default), add, remove and set-role.
func newMembersCommand(sc membershipScope, target *hubTarget) *cobra.Command {
	output := newOutputFlags(outputJSON, outputYAML, outputName)
	cmd := &cobra.Command{
		Use:     "members",
		Aliases: []string{"member"},
		Short:   fmt.Sprintf("List and change who has access to the %s", sc.noun),
		Long: fmt.Sprintf(`Membership is the RBAC unit of a faros %[1]s: every member is either an
admin (may manage members and settings) or a member. Admins of an
organization can manage every workspace in it.

  faros %[2]s members                     # who has access, and as what
  faros %[2]s members add alice@example.com --role member
  faros %[2]s members add bob@example.com --role admin --invite   # not signed up yet
  faros %[2]s members set-role alice@example.com admin
  faros %[2]s members remove bob@example.com`, sc.noun, strings.TrimSuffix(sc.noun, "anization")),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := sc.session(ctx, *target)
			if err != nil {
				return err
			}
			members, err := sc.list(ctx, s)
			if err != nil {
				return err
			}
			return printMembers(cmd.OutOrStdout(), output, sc, s, members)
		},
	}
	output.addFlag(cmd)

	list := *cmd
	list.Use = "list"
	list.Aliases = []string{"ls"}
	list.Short = fmt.Sprintf("List %s members", sc.noun)
	list.Long = ""
	cmd.AddCommand(&list)

	cmd.AddCommand(newMembersAddCommand(sc, target), newMembersRemoveCommand(sc, target), newMembersSetRoleCommand(sc, target))
	return cmd
}

func printMembers(w io.Writer, output *outputFlags, sc membershipScope, s *hubSession, members []memberView) error {
	switch {
	case output.structured():
		return output.printStructured(w, members)
	case output.names():
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.label()
		}
		return printNames(w, names)
	}
	if len(members) == 0 {
		_, err := fmt.Fprintf(w, "No members in %s.\n", sc.where(s))
		return err
	}
	t := &table{headers: []string{"MEMBER", "ROLE", "NAME", "USER ID"}}
	for _, m := range members {
		t.add(m.label(), m.Role, m.UserDisplayName, m.User)
	}
	return t.write(w)
}

func newMembersAddCommand(sc membershipScope, target *hubTarget) *cobra.Command {
	var role string
	var invite bool
	cmd := &cobra.Command{
		Use:   "add <user>",
		Short: fmt.Sprintf("Add a member to the %s (admin only)", sc.noun),
		Long: fmt.Sprintf(`Add an existing user to the %s by email, user id or RBAC identity.
With --invite an unknown email pre-provisions a pending account that the
first sign-in with that email adopts, so access is ready before they arrive.`, sc.noun),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRole(role); err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			s, err := sc.session(ctx, *target)
			if err != nil {
				return err
			}
			var added memberView
			body := map[string]any{"user": args[0], "role": role}
			if invite {
				body["invite"] = true
			}
			if err := s.do(ctx, http.MethodPost, sc.url(s), body, &added); err != nil {
				return fmt.Errorf("adding member: %w", err)
			}
			label := added.label()
			if label == "" {
				label = args[0]
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Added %s to %s as %s.\n", label, sc.where(s), role)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", roleMember, "Role to grant: admin or member")
	cmd.Flags().BoolVar(&invite, "invite", false, "Pre-provision an account for an email that has not signed in yet")
	_ = cmd.RegisterFlagCompletionFunc("role", completeRoles)
	return cmd
}

func newMembersRemoveCommand(sc membershipScope, target *hubTarget) *cobra.Command {
	var yes, cascade bool
	cmd := &cobra.Command{
		Use:     "remove <user>",
		Aliases: []string{"rm", "delete"},
		Short:   fmt.Sprintf("Remove a member from the %s (admin only)", sc.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := sc.session(ctx, *target)
			if err != nil {
				return err
			}
			members, err := sc.list(ctx, s)
			if err != nil {
				return err
			}
			m, err := resolveMember(members, args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirm(cmd, fmt.Sprintf("Remove %s from %s?", m.label(), sc.where(s)))
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("aborted")
				}
			}
			url := sc.url(s) + "/" + m.User
			if cascade && !sc.workspace {
				url += "?cascade=true"
			}
			if err := s.do(ctx, http.MethodDelete, url, nil, nil); err != nil {
				return fmt.Errorf("removing member: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s.\n", m.label(), sc.where(s))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	if !sc.workspace {
		cmd.Flags().BoolVar(&cascade, "cascade", false, "Also remove the user from every workspace of the organization")
	}
	return cmd
}

func newMembersSetRoleCommand(sc membershipScope, target *hubTarget) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-role <user> <admin|member>",
		Short: fmt.Sprintf("Change a member's role in the %s (admin only)", sc.noun),
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 1 {
				return completeRoles(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			role := args[1]
			if err := validateRole(role); err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			s, err := sc.session(ctx, *target)
			if err != nil {
				return err
			}
			members, err := sc.list(ctx, s)
			if err != nil {
				return err
			}
			m, err := resolveMember(members, args[0])
			if err != nil {
				return err
			}
			if m.Role == role {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s is already %s in %s.\n", m.label(), role, sc.where(s))
				return nil
			}
			if err := s.do(ctx, http.MethodPatch, sc.url(s)+"/"+m.User, map[string]string{"role": role}, nil); err != nil {
				return fmt.Errorf("changing role: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s is now %s in %s.\n", m.label(), role, sc.where(s))
			return nil
		},
	}
	return cmd
}
