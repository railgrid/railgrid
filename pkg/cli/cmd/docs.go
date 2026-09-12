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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// newDocsCommand generates the CLI reference (docs/cli/*.md). It is hidden:
// 'make docs-cli' runs it and 'make verify-docs-cli' fails CI when the
// committed reference is stale.
func newDocsCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "docs",
		Short:  "Generate the CLI reference documentation (markdown)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return generateDocs(cmd.Root(), dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "docs/cli", "Directory to write the markdown files into")
	return cmd
}

// generateDocs writes one markdown page per visible command plus an index
// that groups the top-level commands the way 'faros --help' does.
func generateDocs(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Regenerate from scratch so pages for removed commands do not linger.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "faros") && strings.HasSuffix(e.Name(), ".md") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	root.DisableAutoGenTag = true
	prepender := func(string) string { return "" }
	linkHandler := func(name string) string { return name }
	if err := doc.GenMarkdownTreeCustom(root, dir, prepender, linkHandler); err != nil {
		return fmt.Errorf("generating markdown: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "README.md"), []byte(renderDocsIndex(root)), 0o644)
}

func renderDocsIndex(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("# faros CLI reference\n\n")
	b.WriteString("Generated from the command tree with `make docs-cli`; do not edit by hand.\n")
	b.WriteString("Every page lists the command's flags, examples and subcommands.\n\n")
	b.WriteString("Global flags: `--kubeconfig` (default `$KUBECONFIG`, then `~/.kube/config`) and\n")
	b.WriteString("`--insecure-skip-tls-verify`. Shell completion: `faros completion --help`.\n\n")

	byGroup := map[string][]*cobra.Command{}
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() || c.IsAdditionalHelpTopicCommand() {
			continue
		}
		byGroup[c.GroupID] = append(byGroup[c.GroupID], c)
	}
	groups := root.Groups()
	titles := map[string]string{}
	order := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		titles[g.ID] = strings.TrimSuffix(g.Title, ":")
		order = append(order, g.ID)
	}
	if len(byGroup[""]) > 0 {
		titles[""] = "Other commands"
		order = append(order, "")
	}
	for _, id := range order {
		cmds := byGroup[id]
		if len(cmds) == 0 {
			continue
		}
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name() < cmds[j].Name() })
		fmt.Fprintf(&b, "## %s\n\n", titles[id])
		for _, c := range cmds {
			writeDocsIndexEntry(&b, c, 0)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func writeDocsIndexEntry(b *strings.Builder, c *cobra.Command, depth int) {
	page := strings.ReplaceAll(c.CommandPath(), " ", "_") + ".md"
	fmt.Fprintf(b, "%s- [%s](%s) — %s\n", strings.Repeat("  ", depth), c.CommandPath(), page, c.Short)
	subs := c.Commands()
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
	for _, s := range subs {
		if !s.IsAvailableCommand() || s.IsAdditionalHelpTopicCommand() {
			continue
		}
		writeDocsIndexEntry(b, s, depth+1)
	}
}
