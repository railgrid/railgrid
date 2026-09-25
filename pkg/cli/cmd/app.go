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

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

// The types below mirror the App Studio REST projections
// (providers/app-studio/api); only the fields the CLI prints are decoded.

type appProjectView struct {
	Name         string               `json:"name"`
	DisplayName  string               `json:"displayName"`
	Description  string               `json:"description,omitempty"`
	Phase        string               `json:"phase,omitempty"`
	Template     string               `json:"template,omitempty"`
	Deleting     bool                 `json:"deleting"`
	Repository   *appRepositoryView   `json:"repository,omitempty"`
	Environments []appEnvironmentView `json:"environments,omitempty"`
	CreatedAt    time.Time            `json:"createdAt"`
	UpdatedAt    *time.Time           `json:"updatedAt,omitempty"`
}

type appRepositoryView struct {
	Ref          string                    `json:"ref"`
	HTMLURL      string                    `json:"htmlURL,omitempty"`
	Status       string                    `json:"status,omitempty"`
	Message      string                    `json:"message,omitempty"`
	Ready        bool                      `json:"ready,omitempty"`
	Commits      []appRepositoryCommitView `json:"commits,omitempty"`
	CommitsError string                    `json:"commitsError,omitempty"`
}

type appRepositoryCommitView struct {
	Name      string    `json:"name"`
	Phase     string    `json:"phase,omitempty"`
	CommitSHA string    `json:"commitSHA,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type appEnvironmentView struct {
	Name     string           `json:"name"`
	Phase    string           `json:"phase,omitempty"`
	Bindings []appBindingView `json:"bindings,omitempty"`
}

type appBindingView struct {
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"previewURL,omitempty"`
}

type appCreateRequest struct {
	Name                     string `json:"name,omitempty"`
	DisplayName              string `json:"displayName,omitempty"`
	Description              string `json:"description,omitempty"`
	Prompt                   string `json:"prompt,omitempty"`
	TemplateName             string `json:"templateName,omitempty"`
	InferDevelopmentTemplate bool   `json:"inferDevelopmentTemplate,omitempty"`
	ExistingRepositoryRef    string `json:"existingRepositoryRef,omitempty"`
}

type appPromotionView struct {
	Instance   string `json:"instance,omitempty"`
	Promotable bool   `json:"promotable"`
	Build      struct {
		Status    string   `json:"status"`
		CommitSHA string   `json:"commitSHA,omitempty"`
		Missing   []string `json:"missing,omitempty"`
		Note      string   `json:"note"`
	} `json:"build"`
	ImmutableProductionInputs []string        `json:"immutableProductionInputs,omitempty"`
	Production                *appBindingView `json:"production,omitempty"`
}

type appPromoteRequest struct {
	Values    map[string]any `json:"values,omitempty"`
	CommitSHA *string        `json:"commitSHA,omitempty"`
}

type appPromoteResponse struct {
	Environment     string `json:"environment"`
	Instance        string `json:"instance"`
	RolloutRevision string `json:"rolloutRevision"`
	CommitSHA       string `json:"commitSHA,omitempty"`
	Components      []struct {
		Name  string `json:"name"`
		Built bool   `json:"built"`
		Image string `json:"image,omitempty"`
	} `json:"components,omitempty"`
}

// appHydrateResponse mirrors POST …/hydrate-workspace.
type appHydrateResponse struct {
	RepositoryRef string   `json:"repositoryRef"`
	Ref           string   `json:"ref,omitempty"`
	CommitSHA     string   `json:"commitSHA,omitempty"`
	Written       []string `json:"written,omitempty"`
	Skipped       []string `json:"skipped,omitempty"`
}

// appSyncResponse mirrors POST …/sync-development: the dev instance and each
// component's dev agent sync reply, plus the files App Studio left out.
type appSyncResponse struct {
	Target struct {
		ResourceName string `json:"ResourceName"`
	} `json:"target"`
	Result map[string]appSyncComponentResult `json:"result"`
}

type appSyncComponentResult struct {
	syncResponse
	Skipped []appSyncSkippedFile `json:"skipped,omitempty"`
}

type appSyncSkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// appSyncOutput is what 'railgrid app sync -o json' prints.
type appSyncOutput struct {
	Hydrate json.RawMessage `json:"hydrate"`
	Sync    json.RawMessage `json:"sync"`
}

type appPublishingView struct {
	Published   bool `json:"published"`
	Publication *struct {
		Mode  string `json:"mode"`
		Host  string `json:"host,omitempty"`
		URL   string `json:"url,omitempty"`
		Ready bool   `json:"ready"`
		Phase string `json:"phase,omitempty"`
		Error string `json:"error,omitempty"`
	} `json:"publication,omitempty"`
	Grants []struct {
		User    string `json:"user"`
		Revoked bool   `json:"revoked"`
	} `json:"grants,omitempty"`
}

func newAppCommand() *cobra.Command {
	var target hubTarget
	cmd := &cobra.Command{
		Use:     "app",
		Aliases: []string{"apps"},
		Short:   "Manage App Studio projects: list, create, status, sync, promote, publish",
		Long: `Manage App Studio projects through the App Studio REST API, as you.

  railgrid app create shop --template application --display-name Shop --wait
  railgrid app status shop
  railgrid app sync shop
  railgrid app promote shop --hostname-prefix shop
  railgrid app publish shop --mode public

Develop with 'railgrid sandbox' against <project>-dev and record commits with
'railgrid commit <repository ref>' (the ref is shown by 'railgrid app status').`,
	}
	target.addFlags(cmd)
	cmd.AddCommand(
		newAppListCommand(&target),
		newAppCreateCommand(&target),
		newAppStatusCommand(&target),
		newAppSyncCommand(&target),
		newAppPromoteCommand(&target),
		newAppPublishCommand(&target),
	)
	return cmd
}

// App Studio's API coordinates. Every operation below is either a read or
// write of a Project CR through the hub's kcp proxy (list, the existence check
// behind `sandbox exec`), or one of the provider's data-plane verbs — kcp
// custom subresources "projects/{verb}" and "studios/{verb}" on its APIExport,
// declared in providers/app-studio/manifest.yaml spec.dataPlane.verbs and
// served by providers/app-studio/api/dataplane_table.go. There is no
// /services/providers/app-studio/api/... facade any more.
const (
	appStudioAPIGroup   = "ai.railgrid.ai"
	appStudioAPIVersion = "v1alpha1"
	projectsResource    = "projects"
	studiosResource     = "studios"
	// appStudioStudioName mirrors aiv1alpha1.StudioName: the per-workspace
	// singleton that workspace-wide verbs (create-project) hang off.
	appStudioStudioName = "studio"
)

// appStudioAPIURL is the tenant kube API base of App Studio's kinds in the
// session's workspace: /clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1.
func appStudioAPIURL(s *hubSession) string {
	return fmt.Sprintf("%s/clusters/%s/apis/%s/%s", s.Hub, url.PathEscape(s.Cluster), appStudioAPIGroup, appStudioAPIVersion)
}

// projectAPIURL is the Project CR itself (a kube GET, authorized by the
// caller's own RBAC on the object).
func projectAPIURL(s *hubSession, name string) string {
	return appStudioAPIURL(s) + "/" + projectsResource + "/" + url.PathEscape(name)
}

// projectVerbURL addresses a data-plane verb on one project:
//
//	/clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1/projects/{name}/{verb}[/{tail}]
//
// tail is the part of the address inside the object (a grant id, a file
// path), which stays out of the verb so one grant covers the set.
func projectVerbURL(s *hubSession, name, verb string, tail ...string) string {
	u := apiurl.ProviderVerbURL(s.Hub, url.PathEscape(s.Cluster), appStudioAPIGroup, appStudioAPIVersion, projectsResource, url.PathEscape(name), verb)
	for _, t := range tail {
		u += "/" + url.PathEscape(t)
	}
	return u
}

// studioVerbURL addresses a workspace-wide verb on the Studio singleton:
//
//	/clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1/studios/studio/{verb}
func studioVerbURL(s *hubSession, verb string) string {
	return apiurl.ProviderVerbURL(s.Hub, url.PathEscape(s.Cluster), appStudioAPIGroup, appStudioAPIVersion, studiosResource, appStudioStudioName, verb)
}

// ensureStudio creates the workspace's Studio when it is missing. A Studio
// verb is authorized against the Studio object, and the provider's gate is a
// real read of it, so in a workspace that has never created a project the
// verb would 404 with nothing to authorize against. Creating the bound CR
// first is the answer: the API server validates it against the CRD and the
// caller's membership, and the provider's reconciler fills in the service
// references afterwards. Already-exists is success.
func ensureStudio(ctx context.Context, s *hubSession) error {
	studiosURL := appStudioAPIURL(s) + "/" + studiosResource
	if err := s.do(ctx, http.MethodGet, studiosURL+"/"+appStudioStudioName, nil, nil); err == nil {
		return nil
	}
	body := map[string]any{
		"apiVersion": appStudioAPIGroup + "/" + appStudioAPIVersion,
		"kind":       "Studio",
		"metadata":   map[string]any{"name": appStudioStudioName},
		"spec":       map[string]any{"search": map[string]any{"size": "small"}, "browser": map[string]any{"size": "small"}},
	}
	if err := s.do(ctx, http.MethodPost, studiosURL, body, nil); err != nil {
		var apiErr *hubAPIError
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
			return nil
		}
		return fmt.Errorf("creating the workspace's App Studio Studio (is App Studio enabled in this workspace?): %w", err)
	}
	return nil
}

// appProjectCR is the Project CR as the API server serves it: what the list
// needs. Anything joined — live instance status, the commit ledger — comes
// from the `view` verb, which is why that verb exists.
type appProjectCR struct {
	Metadata struct {
		Name              string    `json:"name"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
		DeletionTimestamp *string   `json:"deletionTimestamp,omitempty"`
	} `json:"metadata"`
	Spec struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Repository  *struct {
			RepositoryRef string `json:"repositoryRef"`
		} `json:"repository"`
		Template *struct {
			Name string `json:"name"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Phase     string     `json:"phase"`
		UpdatedAt *time.Time `json:"updatedAt"`
	} `json:"status"`
}

// appProjectFromCR projects a Project CR onto the fields `app list` prints.
func appProjectFromCR(cr appProjectCR) appProjectView {
	p := appProjectView{
		Name:        cr.Metadata.Name,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Phase:       cr.Status.Phase,
		Deleting:    cr.Metadata.DeletionTimestamp != nil,
		CreatedAt:   cr.Metadata.CreationTimestamp,
		UpdatedAt:   cr.Status.UpdatedAt,
	}
	if p.DisplayName == "" {
		p.DisplayName = cr.Metadata.Name
	}
	if cr.Spec.Template != nil {
		p.Template = cr.Spec.Template.Name
	}
	if cr.Spec.Repository != nil && cr.Spec.Repository.RepositoryRef != "" {
		p.Repository = &appRepositoryView{Ref: cr.Spec.Repository.RepositoryRef}
	}
	return p
}

func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func newAppListCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List App Studio projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			// Projects are Project CRs: a kube list through the hub's kcp
			// proxy, authorized by the caller's own RBAC on the kind.
			var list listResponse[appProjectCR]
			if err := s.do(ctx, http.MethodGet, appStudioAPIURL(s)+"/"+projectsResource, nil, &list); err != nil {
				return err
			}
			items := make([]appProjectView, 0, len(list.Items))
			for _, cr := range list.Items {
				items = append(items, appProjectFromCR(cr))
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), listResponse[appProjectView]{Items: items})
			}
			return printAppList(cmd.OutOrStdout(), items)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func printAppList(w io.Writer, items []appProjectView) error {
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "No projects found.")
		return err
	}
	tw := newTabWriter(w)
	printRow(tw, "NAME", "DISPLAY NAME", "PHASE", "TEMPLATE", "REPOSITORY", "AGE")
	for _, p := range items {
		phase := p.Phase
		if p.Deleting {
			phase = "Deleting"
		}
		repo := ""
		if p.Repository != nil {
			repo = p.Repository.Ref
		}
		age := "-"
		if !p.CreatedAt.IsZero() {
			age = formatAge(p.CreatedAt)
		}
		printRow(tw, p.Name, formatStringOrDash(p.DisplayName), formatStringOrDash(phase), formatStringOrDash(p.Template), formatStringOrDash(repo), age)
	}
	return tw.Flush()
}

func newAppCreateCommand(target *hubTarget) *cobra.Command {
	var req appCreateRequest
	var output string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a project (repository, scaffold commit and dev instance)",
		Long: `Create an App Studio project. One call creates the code Repository, the
GitHub repo, the scaffold commit and the <name>-dev instance.

The name is used as given for the project and its code Repository; it is never
suffixed. If a Repository with that name already exists (often one left behind
by a deleted project, which keeps its repository), the hub answers 409 Conflict:
choose another name. Without a validated Git connection the project starts with
no repository, and one connected later gets a suffixed name, so read the
repository ref from 'railgrid app status'. With --wait the command returns
once the repository is ready and the scaffold commit has succeeded — the point
from which cloning and 'railgrid commit' work. Without --template, --prompt lets
App Studio infer the template. --existing-repository adopts a code Repository
you created first (one that names an existing GitHub repo): the project
hydrates from its default branch instead of getting a scaffold, and nothing in
that repository's history is promotable until the first 'railgrid commit'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			req.Name = args[0]
			if req.TemplateName == "" {
				if req.Prompt == "" {
					return fmt.Errorf("--template is required (or pass --prompt to let App Studio choose)")
				}
				req.InferDevelopmentTemplate = true
			}
			return runAppCreate(cmdContext(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr(), *target, req, output, wait, timeout)
		},
	}
	cmd.Flags().StringVar(&req.TemplateName, "template", "", "Development template (e.g. application)")
	cmd.Flags().StringVar(&req.DisplayName, "display-name", "", "Display name")
	cmd.Flags().StringVar(&req.Description, "description", "", "Description")
	cmd.Flags().StringVar(&req.Prompt, "prompt", "", "What to build; does not start an assistant turn")
	cmd.Flags().StringVar(&req.ExistingRepositoryRef, "existing-repository", "", "Adopt this code Repository instead of creating one")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the repository and the scaffold commit")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "How long --wait waits")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func runAppCreate(ctx context.Context, out, errOut io.Writer, target hubTarget, req appCreateRequest, output string, wait bool, timeout time.Duration) error {
	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	if err := ensureStudio(ctx, s); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := s.do(ctx, http.MethodPost, studioVerbURL(s, "create-project"), req, &raw); err != nil {
		// The hub never renames: a taken project or Repository name is a 409
		// whose message says what collided and what to do.
		var apiErr *hubAPIError
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
			return fmt.Errorf("project %q not created (HTTP 409): %s", req.Name, apiErr.Message)
		}
		return err
	}
	var p appProjectView
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decoding project: %w", err)
	}
	if wait {
		_, _ = fmt.Fprintf(errOut, "railgrid app: waiting for repository and scaffold commit of %s…\n", p.Name)
		deadline := time.Now().Add(timeout)
		for !appScaffolded(p) {
			if time.Now().After(deadline) {
				return fmt.Errorf("project %s: repository not ready with a succeeded commit after %s; check 'railgrid app status %s'", p.Name, timeout, p.Name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * sandboxPollInterval):
			}
			raw = nil
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, p.Name, "view"), nil, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return fmt.Errorf("decoding project: %w", err)
			}
		}
	}
	if output == "json" {
		return printJSON(out, raw)
	}
	repo := "-"
	if p.Repository != nil && p.Repository.Ref != "" {
		repo = p.Repository.Ref
	}
	_, err = fmt.Fprintf(out, "project %s created (phase %s, template %s, repository %s)\n",
		p.Name, formatStringOrDash(p.Phase), formatStringOrDash(p.Template), repo)
	return err
}

// appScaffolded is the gate for cloning and committing: phase Ready is not
// enough, the repository must be ready and carry a succeeded commit.
func appScaffolded(p appProjectView) bool {
	if p.Repository == nil || !p.Repository.Ready {
		return false
	}
	for _, c := range p.Repository.Commits {
		if c.Phase == "Succeeded" {
			return true
		}
	}
	return false
}

// appStatus is what `railgrid app status` gathers; -o json prints it whole.
type appStatus struct {
	Project         json.RawMessage `json:"project"`
	Promotion       json.RawMessage `json:"promotion,omitempty"`
	PromotionError  string          `json:"promotionError,omitempty"`
	Publishing      json.RawMessage `json:"publishing,omitempty"`
	PublishingError string          `json:"publishingError,omitempty"`
}

func newAppStatusCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show a project's repository, commits, promotion and publishing state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			st := appStatus{}
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, args[0], "view"), nil, &st.Project); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, args[0], "promotion"), nil, &st.Promotion); err != nil {
				st.PromotionError = err.Error()
			}
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, args[0], "publishing"), nil, &st.Publishing); err != nil {
				st.PublishingError = err.Error()
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), st)
			}
			return printAppStatus(cmd.OutOrStdout(), st, time.Now())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

// printAppStatus renders the compact human summary of a project.
func printAppStatus(w io.Writer, st appStatus, now time.Time) error {
	var p appProjectView
	if err := json.Unmarshal(st.Project, &p); err != nil {
		return fmt.Errorf("decoding project: %w", err)
	}
	tw := newTabWriter(w)
	title := p.Name
	if p.DisplayName != "" && p.DisplayName != p.Name {
		title += " (" + p.DisplayName + ")"
	}
	phase := p.Phase
	if p.Deleting {
		phase = "Deleting"
	}
	printRow(tw, "Project:", fmt.Sprintf("%s  phase=%s  template=%s", title, formatStringOrDash(phase), formatStringOrDash(p.Template)))
	if r := p.Repository; r != nil {
		line := fmt.Sprintf("%s  ready=%v", formatStringOrDash(r.Ref), r.Ready)
		if r.HTMLURL != "" {
			line += "  " + r.HTMLURL
		}
		if !r.Ready && r.Message != "" {
			line += "  (" + oneLine(r.Message, 80) + ")"
		}
		printRow(tw, "Repository:", line)
		if hint := repositoryStallHint(p, now); hint != "" {
			printRow(tw, "", hint)
		}
		for i, c := range latestCommits(r.Commits, 3) {
			label := ""
			if i == 0 {
				label = "Commits:"
			}
			sha := c.CommitSHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			age := ""
			if !c.CreatedAt.IsZero() {
				age = formatAgeAt(now, c.CreatedAt) + " ago"
			}
			printRow(tw, label, strings.TrimSpace(fmt.Sprintf("%s %-9s %s  %s", formatStringOrDash(sha), c.Phase, oneLine(c.Message, 60), age)))
		}
		if r.CommitsError != "" {
			printRow(tw, "", "commits unavailable: "+oneLine(r.CommitsError, 80))
		}
	} else {
		printRow(tw, "Repository:", "-")
	}
	printRow(tw, "Dev URL:", formatStringOrDash(environmentURL(p.Environments, "development")))

	if len(st.Promotion) > 0 {
		var pr appPromotionView
		if err := json.Unmarshal(st.Promotion, &pr); err == nil {
			line := fmt.Sprintf("promotable=%v  build=%s", pr.Promotable, formatStringOrDash(pr.Build.Status))
			if pr.Build.CommitSHA != "" {
				sha := pr.Build.CommitSHA
				if len(sha) > 7 {
					sha = sha[:7]
				}
				line += "  commit=" + sha
			}
			if len(pr.Build.Missing) > 0 {
				line += "  missing=" + strings.Join(pr.Build.Missing, ",")
			}
			printRow(tw, "Promotion:", line)
			prod := "- (never promoted)"
			if pr.Production != nil {
				prod = fmt.Sprintf("%s  %s", formatStringOrDash(pr.Production.Phase), formatStringOrDash(pr.Production.URL))
				if pr.Production.Phase == "" && pr.Production.URL == "" {
					// The binding exists but its status trails the Instance by
					// a few seconds after a promote; "-  -" read as a failure.
					prod = "- (promoted; the production instance has not reported yet, re-run in a few seconds)"
				}
			}
			printRow(tw, "Production:", prod)
		}
	} else if st.PromotionError != "" {
		printRow(tw, "Promotion:", "unavailable: "+oneLine(st.PromotionError, 100))
	}

	if len(st.Publishing) > 0 {
		var pub appPublishingView
		if err := json.Unmarshal(st.Publishing, &pub); err == nil {
			printRow(tw, "Publishing:", publishingSummary(pub))
		}
	} else if st.PublishingError != "" {
		printRow(tw, "Publishing:", "unavailable: "+oneLine(st.PublishingError, 100))
	}
	return tw.Flush()
}

func publishingSummary(pub appPublishingView) string {
	if !pub.Published || pub.Publication == nil {
		return "private"
	}
	line := pub.Publication.Mode
	if pub.Publication.URL != "" {
		line += "  " + pub.Publication.URL
	}
	if !pub.Publication.Ready {
		line += fmt.Sprintf("  (not ready: %s)", formatStringOrDash(pub.Publication.Phase))
	}
	if pub.Publication.Error != "" {
		line += "  error: " + oneLine(pub.Publication.Error, 80)
	}
	active := 0
	for _, g := range pub.Grants {
		if !g.Revoked {
			active++
		}
	}
	if active > 0 {
		line += fmt.Sprintf("  grants=%d", active)
	}
	return line
}

// repositoryStallThreshold is how long a fresh project's repository may stay
// not-ready before `railgrid app status` stops calling it latency. Measured on a
// dev hub the repository is ready in ~10 s and the scaffold commit lands
// within 40 s; anything past two minutes with no status at all means the
// code provider is not reconciling.
const repositoryStallThreshold = 2 * time.Minute

// repositoryStallHint names the one failure a newcomer cannot tell from
// latency: a repository that has stayed not-ready, with no status message and
// no commit, since the project was created. It returns "" when the repository
// is ready, still young, or reports a message of its own.
func repositoryStallHint(p appProjectView, now time.Time) string {
	r := p.Repository
	if r == nil || r.Ready || r.Message != "" || len(r.Commits) > 0 || p.CreatedAt.IsZero() {
		return ""
	}
	age := now.Sub(p.CreatedAt)
	if age < repositoryStallThreshold {
		return ""
	}
	return fmt.Sprintf("not ready for %s with no status: the code provider is not reconciling (kubectl get repositories.code.railgrid.ai %s -o yaml has no status); wait for the operator, don't recreate the project",
		age.Truncate(time.Minute), formatStringOrDash(r.Ref))
}

// latestCommits returns up to n commits, newest first.
func latestCommits(commits []appRepositoryCommitView, n int) []appRepositoryCommitView {
	sorted := append([]appRepositoryCommitView(nil), commits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// environmentURL returns the first URL (or preview URL) bound in the named
// environment.
func environmentURL(envs []appEnvironmentView, name string) string {
	for _, env := range envs {
		if env.Name != name {
			continue
		}
		for _, b := range env.Bindings {
			if b.URL != "" {
				return b.URL
			}
			if b.PreviewURL != "" {
				return b.PreviewURL
			}
		}
	}
	return ""
}

func formatAgeAt(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func newAppSyncCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "sync <name>",
		Short: "Load the repository into the project workspace and sync it to <name>-dev",
		Long: `Hydrate the project workspace from its repository's default branch, then run
App Studio's authoritative development sync, which pushes the workspace to
every component of the <name>-dev instance. Afterwards 'railgrid sandbox exec'
works against <name>-dev.

Use this rather than 'railgrid sandbox sync' on an App Studio dev instance: App
Studio owns that instance's file set, and a sandbox sync replaces it. Files
the sync left out (binaries a component's dev agent cannot take, files over
the size limits) are listed per component.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			return runAppSync(cmdContext(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr(), *target, args[0], output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func runAppSync(ctx context.Context, out, errOut io.Writer, target hubTarget, name, output string) error {
	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	var res appSyncOutput
	_, _ = fmt.Fprintf(errOut, "railgrid app: loading %s's workspace from its repository…\n", name)
	if err := s.do(ctx, http.MethodPost, projectVerbURL(s, name, "hydrate-workspace"), map[string]any{}, &res.Hydrate); err != nil {
		return fmt.Errorf("hydrating the workspace: %w", err)
	}
	_, _ = fmt.Fprintf(errOut, "railgrid app: syncing %s's workspace to its development instance…\n", name)
	if err := s.do(ctx, http.MethodPost, projectVerbURL(s, name, "sync-development"), map[string]any{}, &res.Sync); err != nil {
		return fmt.Errorf("syncing the development instance: %w", err)
	}
	if output == "json" {
		return printJSON(out, res)
	}
	return printAppSync(out, res)
}

// printAppSync renders the hydrate and per-component sync summary.
func printAppSync(w io.Writer, res appSyncOutput) error {
	var hydrate appHydrateResponse
	if len(res.Hydrate) > 0 {
		if err := json.Unmarshal(res.Hydrate, &hydrate); err != nil {
			return fmt.Errorf("decoding hydrate response: %w", err)
		}
	}
	var synced appSyncResponse
	if len(res.Sync) > 0 {
		if err := json.Unmarshal(res.Sync, &synced); err != nil {
			return fmt.Errorf("decoding sync response: %w", err)
		}
	}
	source := formatStringOrDash(hydrate.RepositoryRef)
	if hydrate.Ref != "" {
		source += "@" + hydrate.Ref
	}
	if sha := hydrate.CommitSHA; sha != "" {
		if len(sha) > 7 {
			sha = sha[:7]
		}
		source += " (" + sha + ")"
	}
	_, _ = fmt.Fprintf(w, "workspace: loaded from %s, %d written, %d skipped\n", source, len(hydrate.Written), len(hydrate.Skipped))
	for _, p := range hydrate.Skipped {
		_, _ = fmt.Fprintf(w, "  skipped %s\n", p)
	}
	components := make([]string, 0, len(synced.Result))
	for c := range synced.Result {
		components = append(components, c)
	}
	sort.Strings(components)
	instance := formatStringOrDash(synced.Target.ResourceName)
	if len(components) == 0 {
		_, _ = fmt.Fprintf(w, "%s: no component results\n", instance)
	}
	binaryUnsupported := false
	for _, c := range components {
		r := synced.Result[c]
		line := fmt.Sprintf("%s/%s: %s, %d changed, %d deleted, restarted=%v, revision %d",
			instance, c, formatStringOrDash(r.Phase), len(r.Changed), len(r.Deleted), r.Restarted, r.SourceRevision)
		if len(r.Skipped) > 0 {
			line += fmt.Sprintf(", %d skipped", len(r.Skipped))
		}
		_, _ = fmt.Fprintln(w, line)
		for _, f := range r.Skipped {
			_, _ = fmt.Fprintf(w, "  skipped %s (%s)\n", f.Path, formatStringOrDash(f.Reason))
			binaryUnsupported = binaryUnsupported || f.Reason == "binary-unsupported"
		}
	}
	if binaryUnsupported {
		_, _ = fmt.Fprintln(w, "binary-unsupported: the component's dev agent does not accept binary files; update the instance to sync them")
	}
	return nil
}

func newAppPromoteCommand(target *hubTarget) *cobra.Command {
	var hostnamePrefix, commitSHA, output string
	cmd := &cobra.Command{
		Use:   "promote <name>",
		Short: "Promote the latest built commit (or --commit) to production",
		Long: `Create or update the <name>-prod instance from a built, railgrid-recorded commit.

The hostname prefix is locked after the first production deploy: pass
--hostname-prefix on the first promote, and later either the same value or
nothing. Each promote rolls pods, even for the same commit.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			req := buildPromoteRequest(hostnamePrefix, commitSHA)
			var raw json.RawMessage
			if err := s.do(ctx, http.MethodPost, projectVerbURL(s, args[0], "promote"), req, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var res appPromoteResponse
			if err := json.Unmarshal(raw, &res); err != nil {
				return fmt.Errorf("decoding promote response: %w", err)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "promoted %s to %s (commit %s, rollout %s)\n", args[0], formatStringOrDash(res.Instance), formatStringOrDash(res.CommitSHA), formatStringOrDash(res.RolloutRevision))
			for _, c := range res.Components {
				_, _ = fmt.Fprintf(w, "  %s built=%v %s\n", c.Name, c.Built, c.Image)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&hostnamePrefix, "hostname-prefix", "", "Production hostname prefix (locked after the first promote)")
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Promote this railgrid-recorded commit instead of the latest")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func buildPromoteRequest(hostnamePrefix, commitSHA string) appPromoteRequest {
	var req appPromoteRequest
	if hostnamePrefix != "" {
		req.Values = map[string]any{"expose": map[string]any{"hostnamePrefix": hostnamePrefix}}
	}
	if commitSHA != "" {
		req.CommitSHA = &commitSHA
	}
	return req
}

// publishSettle bounds how long `app publish` re-reads the publishing state
// after the POST. The POST answers before the production Instance reports the
// new access mode, so its "(not ready: Pending)" was stale for an app that was
// already serving. Variables so tests can shorten them.
var (
	publishSettleTimeout  = 15 * time.Second
	publishSettleInterval = 2 * time.Second
)

// settlePublishing re-reads GET …/publishing until the publication is ready or
// the timeout passes, and returns the latest view. Read errors keep the last
// good view: the publish itself already succeeded.
func settlePublishing(ctx context.Context, s *hubSession, name string, pub appPublishingView) appPublishingView {
	deadline := time.Now().Add(publishSettleTimeout)
	for pub.Publication != nil && !pub.Publication.Ready && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return pub
		case <-time.After(publishSettleInterval):
		}
		var next appPublishingView
		if err := s.do(ctx, http.MethodGet, projectVerbURL(s, name, "publishing"), nil, &next); err == nil {
			pub = next
		}
	}
	return pub
}

func newAppPublishCommand(target *hubTarget) *cobra.Command {
	var mode, output string
	cmd := &cobra.Command{
		Use:   "publish <name>",
		Short: "Set production visibility: public, restricted or private",
		Long: `Set who can open the production app.

  public      anyone with the URL
  restricted  signed-in users you grant (the default after the first promote)
  private     unpublish and drop every grant`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			method := http.MethodPost
			var body any
			switch mode {
			case "public", "restricted":
				body = map[string]string{"mode": mode}
			case "private":
				method = http.MethodDelete
			default:
				return fmt.Errorf("--mode must be public, restricted or private")
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := s.do(ctx, method, projectVerbURL(s, args[0], "publishing"), body, &raw); err != nil {
				return err
			}
			if output == "json" {
				if len(raw) == 0 {
					raw = json.RawMessage("{}")
				}
				return printJSON(cmd.OutOrStdout(), raw)
			}
			summary := "private (unpublished; anonymous requests are redirected to sign-in, your own app tokens still work)"
			if mode != "private" && len(raw) > 0 {
				var pub appPublishingView
				if err := json.Unmarshal(raw, &pub); err == nil {
					pub = settlePublishing(ctx, s, args[0], pub)
					summary = publishingSummary(pub)
					if mode == "public" && pub.Publication != nil && pub.Publication.Ready {
						summary += "  (anonymous requests may still be redirected to sign-in for ~20s)"
					}
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", args[0], summary)
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "public, restricted or private (required)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	_ = cmd.MarkFlagRequired("mode")
	return cmd
}
