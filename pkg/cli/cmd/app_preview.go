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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// appPreviewView mirrors App Studio's preview verb response.
type appPreviewView struct {
	Mode      string `json:"mode"`
	URL       string `json:"url,omitempty"`
	Converged bool   `json:"converged"`
	Supported bool   `json:"supported"`
	Grants    []struct {
		User    string `json:"user"`
		Revoked bool   `json:"revoked"`
	} `json:"grants,omitempty"`
}

// appCheckpointsView mirrors the checkpoints verb: one item per stage of the
// path to production, each with the reason it is not done.
type appCheckpointsView struct {
	Items []appCheckpointItem `json:"items"`
}

type appCheckpointItem struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Remediation *struct {
		Kind      string `json:"kind,omitempty"`
		Tool      string `json:"tool,omitempty"`
		ActionURL string `json:"actionUrl,omitempty"`
		Message   string `json:"message,omitempty"`
	} `json:"remediation,omitempty"`
}

func newAppPreviewCommand(target *hubTarget) *cobra.Command {
	var mode, output string
	cmd := &cobra.Command{
		Use:   "preview <name>",
		Short: "Show or set who can open the development preview (Dev URL)",
		Long: `Read or change the development preview's access mode.

  restricted  signed-in workspace members and people you grant (the default)
  public      anyone with the Dev URL, no sign-in
  private     back to restricted and drop every preview grant

Without --mode the current mode is printed. The preview is the live
development sandbox, not a built image: good for a demo, not a deployment.`,
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
			method := http.MethodGet
			var body any
			switch mode {
			case "":
			case "public", "restricted":
				method = http.MethodPost
				body = map[string]string{"mode": mode}
			case "private":
				method = http.MethodDelete
			default:
				return fmt.Errorf("--mode must be public, restricted or private")
			}
			var raw json.RawMessage
			if err := s.do(ctx, method, projectVerbURL(s, args[0], "preview"), body, &raw); err != nil {
				return err
			}
			if output == "json" {
				if len(raw) == 0 {
					raw = json.RawMessage("{}")
				}
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var view appPreviewView
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &view); err != nil {
					return fmt.Errorf("decoding preview: %w", err)
				}
			}
			line := previewSummary(view)
			if mode == "public" && !view.Converged {
				line += "  (anonymous requests may still be redirected to sign-in for ~20s)"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", args[0], line)
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "public, restricted or private; omit to show the current mode")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

// previewSummary is the one-line form of the preview state used by both
// 'app preview' and 'app status'.
func previewSummary(view appPreviewView) string {
	if !view.Supported {
		return "- (no development preview)"
	}
	line := formatStringOrDash(view.Mode)
	if view.URL != "" {
		line += "  " + view.URL
	}
	if !view.Converged {
		line += "  (converging)"
	}
	if n := len(view.Grants); n > 0 {
		line += fmt.Sprintf("  grants=%d", n)
	}
	return line
}

func newAppCheckpointsCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "checkpoints <name>",
		Short: "Show each stage on the way to production and why it is not done yet",
		Long: `List the project's checkpoints (template, git, source, production) with the
state of each and, for anything not done, the reason and the suggested fix.
This is where a blocked promotion explains itself; 'railgrid app status' prints
the same reasons under Blocked:.`,
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
			var raw json.RawMessage
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, args[0], "checkpoints"), nil, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var view appCheckpointsView
			if err := json.Unmarshal(raw, &view); err != nil {
				return fmt.Errorf("decoding checkpoints: %w", err)
			}
			return printAppCheckpoints(cmd.OutOrStdout(), view)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func printAppCheckpoints(w io.Writer, view appCheckpointsView) error {
	tw := newTabWriter(w)
	for _, it := range view.Items {
		label := it.Label
		if label == "" {
			label = it.Key
		}
		line := formatStringOrDash(it.State)
		if it.Reason != "" {
			line += "  " + oneLine(it.Reason, 160)
		}
		printRow(tw, label+":", line)
		if it.Remediation != nil && it.State != "done" {
			fix := strings.TrimSpace(it.Remediation.Message)
			if fix == "" && it.Remediation.Tool != "" {
				fix = it.Remediation.Tool
			}
			if fix != "" {
				printRow(tw, "", "fix: "+oneLine(fix, 160))
			}
		}
	}
	return tw.Flush()
}

// blockedCheckpoints returns the items that are not done, for the status
// summary: "<label>: <state> — <reason>".
func blockedCheckpoints(view appCheckpointsView) []string {
	var lines []string
	for _, it := range view.Items {
		if it.State == "done" || it.State == "" {
			continue
		}
		label := it.Label
		if label == "" {
			label = it.Key
		}
		line := fmt.Sprintf("%s %s", label, it.State)
		if it.Reason != "" {
			line += ": " + oneLine(it.Reason, 140)
		}
		lines = append(lines, line)
	}
	return lines
}
