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
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
		Short:   "Manage App Studio projects: list, create, status, promote, publish",
		Long: `Manage App Studio projects through the App Studio REST API, as you.

  faros app create shop --template application --display-name Shop --wait
  faros app status shop
  faros app promote shop --hostname-prefix shop
  faros app publish shop --mode public

Develop with 'faros sandbox' against <project>-dev and record commits with
'faros commit <repository ref>' (the ref is shown by 'faros app status').`,
	}
	target.addFlags(cmd)
	cmd.AddCommand(
		newAppListCommand(&target),
		newAppCreateCommand(&target),
		newAppStatusCommand(&target),
		newAppPromoteCommand(&target),
		newAppPublishCommand(&target),
	)
	return cmd
}

func projectURL(s *hubSession, name string, sub ...string) string {
	u := s.appStudioURL() + "/api/projects/" + url.PathEscape(name)
	if len(sub) > 0 {
		u += "/" + strings.Join(sub, "/")
	}
	return u
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
			var raw json.RawMessage
			if err := s.do(ctx, http.MethodGet, s.appStudioURL()+"/api/projects", nil, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var list listResponse[appProjectView]
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("decoding projects: %w", err)
			}
			return printAppList(cmd.OutOrStdout(), list.Items)
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

A taken name silently gets a suffix, and the repository name is not always the
project name: read it from 'faros app status'. With --wait the command returns
once the repository is ready and the scaffold commit has succeeded — the point
from which cloning and 'faros commit' work. Without --template, --prompt lets
App Studio infer the template.`,
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
	var raw json.RawMessage
	if err := s.do(ctx, http.MethodPost, s.appStudioURL()+"/api/projects", req, &raw); err != nil {
		return err
	}
	var p appProjectView
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decoding project: %w", err)
	}
	if p.Name != "" && p.Name != req.Name {
		_, _ = fmt.Fprintf(errOut, "faros app: name %q was taken; created %q\n", req.Name, p.Name)
	}
	if wait {
		_, _ = fmt.Fprintf(errOut, "faros app: waiting for repository and scaffold commit of %s…\n", p.Name)
		deadline := time.Now().Add(timeout)
		for !appScaffolded(p) {
			if time.Now().After(deadline) {
				return fmt.Errorf("project %s: repository not ready with a succeeded commit after %s; check 'faros app status %s'", p.Name, timeout, p.Name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * sandboxPollInterval):
			}
			raw = nil
			if err := s.do(ctx, http.MethodGet, projectURL(s, p.Name), nil, &raw); err != nil {
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

// appStatus is what `faros app status` gathers; -o json prints it whole.
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
			if err := s.do(ctx, http.MethodGet, projectURL(s, args[0]), nil, &st.Project); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodGet, projectURL(s, args[0], "promotion"), nil, &st.Promotion); err != nil {
				st.PromotionError = err.Error()
			}
			if err := s.do(ctx, http.MethodGet, projectURL(s, args[0], "publishing"), nil, &st.Publishing); err != nil {
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

func newAppPromoteCommand(target *hubTarget) *cobra.Command {
	var hostnamePrefix, commitSHA, output string
	cmd := &cobra.Command{
		Use:   "promote <name>",
		Short: "Promote the latest built commit (or --commit) to production",
		Long: `Create or update the <name>-prod instance from a built, faros-recorded commit.

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
			if err := s.do(ctx, http.MethodPost, projectURL(s, args[0], "promote"), req, &raw); err != nil {
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
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Promote this faros-recorded commit instead of the latest")
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
			if err := s.do(ctx, method, projectURL(s, args[0], "publishing"), body, &raw); err != nil {
				return err
			}
			if output == "json" {
				if len(raw) == 0 {
					raw = json.RawMessage("{}")
				}
				return printJSON(cmd.OutOrStdout(), raw)
			}
			summary := "private"
			if len(raw) > 0 {
				var pub appPublishingView
				if err := json.Unmarshal(raw, &pub); err == nil {
					summary = publishingSummary(pub)
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
