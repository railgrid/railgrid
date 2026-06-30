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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// kedgeContextName is the kubeconfig context that `kedge login` writes and
// that `kedge use` retargets.
const kedgeContextName = "kedge"

// orgView / workspaceView / listResponse mirror the hub REST projections in
// pkg/hub/restapi (OrgView, WorkspaceView, ListResponse). Only the fields the
// switcher needs are decoded.
type orgView struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	Personal    bool   `json:"personal"`
}

type workspaceView struct {
	UUID        string `json:"uuid"`
	OrgUUID     string `json:"orgUUID"`
	DisplayName string `json:"displayName"`
	ClusterName string `json:"clusterName"`
}

type listResponse[T any] struct {
	Items []T `json:"items"`
}

func newUseCommand() *cobra.Command {
	var orgFlag, wsFlag string

	cmd := &cobra.Command{
		Use:   "use",
		Short: "Switch the active organization and workspace",
		Long: `Switch the kubeconfig "kedge" context between the organizations and
workspaces you belong to.

With no flags it opens an interactive picker — first an organization, then a
workspace within it. Pass --org and/or --workspace (display name or UUID) to
skip the picker, e.g. for scripts:

  kedge use                                  # fully interactive
  kedge use --org acme                       # pick a workspace in "acme"
  kedge use --org acme --workspace platform  # non-interactive`,
		Aliases: []string{"switch", "ctx"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUse(cmd.Context(), orgFlag, wsFlag)
		},
	}

	cmd.Flags().StringVar(&orgFlag, "org", "", "Organization display name or UUID (skips the org picker)")
	cmd.Flags().StringVar(&wsFlag, "workspace", "", "Workspace display name or UUID (skips the workspace picker)")

	return cmd
}

func runUse(ctx context.Context, orgFlag, wsFlag string) error {
	// Load the kubeconfig and locate the kedge context to retarget.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	raw, err := loadingRules.GetStartingConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	ctxName, kctx, err := resolveKedgeContext(raw)
	if err != nil {
		return err
	}
	cluster := raw.Clusters[kctx.Cluster]
	if cluster == nil {
		return fmt.Errorf("kubeconfig context %q references missing cluster %q", ctxName, kctx.Cluster)
	}
	base, _ := apiurl.SplitBaseAndCluster(cluster.Server)

	// Build an authenticated HTTP client from the context. rest.TransportFor
	// wires up the exec OIDC credential plugin (or static token) and the TLS
	// settings, so REST calls carry the same identity kubectl uses.
	clientConfig := clientcmd.NewNonInteractiveClientConfig(*raw, ctxName, &clientcmd.ConfigOverrides{}, loadingRules)
	restCfg, err := clientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("building client config: %w", err)
	}
	if globalInsecureTLS {
		restCfg.Insecure = true
		restCfg.CAData = nil
		restCfg.CAFile = ""
	}
	transport, err := rest.TransportFor(restCfg)
	if err != nil {
		return fmt.Errorf("building HTTP transport: %w", err)
	}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	// Interactive selection needs a TTY; bail early with actionable advice
	// when one isn't available and a flag is missing.
	if (orgFlag == "" || wsFlag == "") && !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("no interactive terminal; pass --org and --workspace to switch non-interactively")
	}

	// 1. Pick the organization.
	orgs, err := fetchOrgs(ctx, httpClient, base)
	if err != nil {
		return err
	}
	if len(orgs) == 0 {
		return fmt.Errorf("you are not a member of any organizations")
	}
	org, err := selectOrg(orgs, orgFlag)
	if err != nil {
		return err
	}

	// 2. Pick the workspace within it.
	workspaces, err := fetchWorkspaces(ctx, httpClient, base, org.UUID)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		return fmt.Errorf("organization %q has no workspaces you can access", org.DisplayName)
	}
	ws, err := selectWorkspace(workspaces, wsFlag)
	if err != nil {
		return err
	}
	if ws.ClusterName == "" {
		return fmt.Errorf("workspace %q is not ready yet (no cluster assigned); try again shortly", displayLabel(ws.DisplayName, ws.UUID))
	}

	// 3. Retarget the kedge cluster server URL and persist.
	newServer := apiurl.HubServerURL(base, ws.ClusterName)
	if cluster.Server == newServer {
		fmt.Printf("Already using organization %q / workspace %q\n", org.DisplayName, displayLabel(ws.DisplayName, ws.UUID))
		return nil
	}
	cluster.Server = newServer

	destPath := loadingRules.GetDefaultFilename()
	if kubeconfig != "" {
		destPath = kubeconfig
	}
	if err := clientcmd.WriteToFile(*raw, destPath); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", destPath, err)
	}

	fmt.Printf("Switched to organization %q / workspace %q\n", org.DisplayName, displayLabel(ws.DisplayName, ws.UUID))
	fmt.Printf("Context %q now points at %s\n", ctxName, newServer)
	return nil
}

// resolveKedgeContext returns the context to retarget: the "kedge" context if
// present (what `kedge login` writes), otherwise the current-context.
func resolveKedgeContext(raw *clientcmdapi.Config) (string, *clientcmdapi.Context, error) {
	if c, ok := raw.Contexts[kedgeContextName]; ok {
		return kedgeContextName, c, nil
	}
	if raw.CurrentContext != "" {
		if c, ok := raw.Contexts[raw.CurrentContext]; ok {
			return raw.CurrentContext, c, nil
		}
	}
	return "", nil, fmt.Errorf("no %q context found in kubeconfig — run 'kedge login' first", kedgeContextName)
}

func fetchOrgs(ctx context.Context, c *http.Client, base string) ([]orgView, error) {
	var resp listResponse[orgView]
	if err := doGetJSON(ctx, c, base+"/api/orgs", "", &resp); err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}
	sort.SliceStable(resp.Items, func(i, j int) bool {
		// Personal org first, then alphabetical by display name.
		if resp.Items[i].Personal != resp.Items[j].Personal {
			return resp.Items[i].Personal
		}
		return strings.ToLower(resp.Items[i].DisplayName) < strings.ToLower(resp.Items[j].DisplayName)
	})
	return resp.Items, nil
}

func fetchWorkspaces(ctx context.Context, c *http.Client, base, orgUUID string) ([]workspaceView, error) {
	var resp listResponse[workspaceView]
	if err := doGetJSON(ctx, c, base+"/api/orgs/"+orgUUID+"/workspaces", orgUUID, &resp); err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	sort.SliceStable(resp.Items, func(i, j int) bool {
		return strings.ToLower(displayLabel(resp.Items[i].DisplayName, resp.Items[i].UUID)) <
			strings.ToLower(displayLabel(resp.Items[j].DisplayName, resp.Items[j].UUID))
	})
	return resp.Items, nil
}

// doGetJSON issues an authenticated GET and decodes a JSON body. When orgHeader
// is set it is sent as X-Kedge-Org, which the tenant-scoped endpoints require.
func doGetJSON(ctx context.Context, c *http.Client, url, orgHeader string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if orgHeader != "" {
		req.Header.Set("X-Kedge-Org", orgHeader)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

func selectOrg(orgs []orgView, flag string) (orgView, error) {
	if flag != "" {
		return matchOrg(orgs, flag)
	}
	items := make([]pickerItem, len(orgs))
	for i, o := range orgs {
		desc := o.UUID
		if o.Personal {
			desc = "personal · " + o.UUID
		}
		items[i] = pickerItem{title: o.DisplayName, desc: desc}
	}
	idx, err := runPicker("Select an organization", items)
	if err != nil {
		return orgView{}, err
	}
	return orgs[idx], nil
}

func selectWorkspace(workspaces []workspaceView, flag string) (workspaceView, error) {
	if flag != "" {
		return matchWorkspace(workspaces, flag)
	}
	items := make([]pickerItem, len(workspaces))
	for i, ws := range workspaces {
		desc := ws.UUID
		if ws.ClusterName == "" {
			desc += " · not ready"
		}
		items[i] = pickerItem{title: displayLabel(ws.DisplayName, ws.UUID), desc: desc}
	}
	idx, err := runPicker("Select a workspace", items)
	if err != nil {
		return workspaceView{}, err
	}
	return workspaces[idx], nil
}

// matchOrg resolves a --org value to a single org by UUID (exact) or display
// name (case-insensitive). Display names are not unique, so an ambiguous name
// is an error that points the user at the disambiguating UUIDs.
func matchOrg(orgs []orgView, q string) (orgView, error) {
	var byName []orgView
	for _, o := range orgs {
		if o.UUID == q {
			return o, nil
		}
		if strings.EqualFold(o.DisplayName, q) {
			byName = append(byName, o)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return orgView{}, fmt.Errorf("no organization matches %q", q)
	default:
		uuids := make([]string, len(byName))
		for i, o := range byName {
			uuids[i] = o.UUID
		}
		return orgView{}, fmt.Errorf("organization %q is ambiguous (%d matches); use a UUID: %s", q, len(byName), strings.Join(uuids, ", "))
	}
}

// matchWorkspace mirrors matchOrg for workspaces. Workspace display names may
// be empty, so a UUID match always wins.
func matchWorkspace(workspaces []workspaceView, q string) (workspaceView, error) {
	var byName []workspaceView
	for _, ws := range workspaces {
		if ws.UUID == q {
			return ws, nil
		}
		if ws.DisplayName != "" && strings.EqualFold(ws.DisplayName, q) {
			byName = append(byName, ws)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return workspaceView{}, fmt.Errorf("no workspace matches %q", q)
	default:
		uuids := make([]string, len(byName))
		for i, ws := range byName {
			uuids[i] = ws.UUID
		}
		return workspaceView{}, fmt.Errorf("workspace %q is ambiguous (%d matches); use a UUID: %s", q, len(byName), strings.Join(uuids, ", "))
	}
}

// displayLabel returns name when set, falling back to uuid for workspaces that
// never had a display name stamped.
func displayLabel(name, uuid string) string {
	if name != "" {
		return name
	}
	return uuid
}
