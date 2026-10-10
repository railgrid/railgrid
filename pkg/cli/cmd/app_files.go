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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// The project workspace file verbs (providers/app-studio/api, "files",
// "files-content", "files-raw"). The path is always the ?path= query
// parameter, relative to the repository root.

type appFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type appFileList struct {
	Files     []appFileEntry `json:"files"`
	Truncated bool           `json:"truncated,omitempty"`
}

type appFileWriteResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Version string `json:"version"`
	Binary  bool   `json:"binary"`
}

func projectFileVerbURL(s *hubSession, name, verb, path string) string {
	return projectVerbURL(s, name, verb) + "?path=" + url.QueryEscape(path)
}

func newAppFilesCommand(target *hubTarget) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "List, read, write and delete files in a project's workspace",
		Long: `Work with the project workspace App Studio keeps between git and the dev
sandbox. A write marks the path uncommitted: the reconciler commits it and
syncs it to <name>-dev, the same as an assistant edit. Paths are relative to
the repository root (web/public/logo.png on the application template).

  railgrid app files ls shop
  railgrid app files get shop api/server.mjs > server.mjs
  railgrid app files put shop web/public/logo.png ./logo.png
  railgrid app files get shop web/public/old.png --version-only
  railgrid app files rm shop web/public/old.png --expected-version <version>`,
	}
	cmd.AddCommand(
		newAppFilesLsCommand(target),
		newAppFilesGetCommand(target),
		newAppFilesPutCommand(target),
		newAppFilesRmCommand(target),
	)
	return cmd
}

func newAppFilesLsCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:     "ls <name>",
		Aliases: []string{"list"},
		Short:   "List the workspace files with their sizes",
		Args:    cobra.ExactArgs(1),
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
			if err := s.do(ctx, http.MethodGet, projectVerbURL(s, args[0], "files"), nil, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var list appFileList
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("decoding file list: %w", err)
			}
			if len(list.Files) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No files in the workspace yet (hydrate it with 'railgrid app sync', or 'railgrid app files put').")
				return err
			}
			tw := newTabWriter(cmd.OutOrStdout())
			for _, f := range list.Files {
				printRow(tw, fmt.Sprintf("%d", f.Size), f.Path)
			}
			if list.Truncated {
				printRow(tw, "", "(list truncated)")
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func newAppFilesGetCommand(target *hubTarget) *cobra.Command {
	var outPath string
	var versionOnly bool
	cmd := &cobra.Command{
		Use:   "get <name> <path>",
		Short: "Print a workspace file (text or binary) to stdout or --out",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if versionOnly && outPath != "" {
				return fmt.Errorf("--version-only and --out cannot be combined")
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			resp, err := s.stream(ctx, http.MethodGet, projectFileVerbURL(s, args[0], "files-raw", args[1]))
			if err != nil {
				return err
			}
			defer resp.Body.Close() //nolint:errcheck
			if versionOnly {
				version := strings.Trim(strings.TrimSpace(resp.Header.Get("ETag")), "\"")
				if version == "" || strings.HasPrefix(version, "W/") {
					return fmt.Errorf("the server did not return an exact file version")
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
				return err
			}
			w := cmd.OutOrStdout()
			if outPath != "" {
				f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
				if err != nil {
					return err
				}
				defer f.Close() //nolint:errcheck
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("reading %s: %w", args[1], err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "Write to this local file instead of stdout")
	cmd.Flags().BoolVar(&versionOnly, "version-only", false, "Print the exact version required to replace or delete this file")
	return cmd
}

func newAppFilesPutCommand(target *hubTarget) *cobra.Command {
	var createOnly bool
	var expectedVersion string
	var output string
	cmd := &cobra.Command{
		Use:   "put <name> <path> [local-file]",
		Short: "Write a workspace file from a local file (or stdin); binaries are fine",
		Long: `Upload one file into the project workspace at <path>. The content comes from
<local-file>, or from stdin when it is omitted or '-'. Limits: 256 KiB for
text, 25 MiB for binary. The reconciler commits the file and syncs it to the
dev sandbox; on the application template only files under web/ and api/
are served.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			var data []byte
			var err error
			if len(args) < 3 || args[2] == "-" {
				data, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 26<<20))
			} else {
				data, err = os.ReadFile(args[2])
			}
			if err != nil {
				return fmt.Errorf("reading the file to upload: %w", err)
			}
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			header := http.Header{}
			header.Set("Content-Type", "application/octet-stream")
			if createOnly && expectedVersion != "" {
				return fmt.Errorf("--create-only and --expected-version cannot be combined")
			}
			if expectedVersion != "" {
				header.Set("If-Match", expectedVersion)
			} else {
				header.Set("If-None-Match", "*")
			}
			var raw json.RawMessage
			if err := s.doWithHeaders(ctx, http.MethodPut, projectFileVerbURL(s, args[0], "files-content", args[1]), bytes.NewReader(data), header, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			var res appFileWriteResult
			_ = json.Unmarshal(raw, &res)
			kind := "text"
			if res.Binary {
				kind = "binary"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, %s, version %s); the reconciler commits it and syncs %s-dev\n",
				res.Path, res.Size, kind, shortDigest(res.Version), args[0])
			return err
		},
	}
	cmd.Flags().BoolVar(&createOnly, "create-only", false, "Create only (the default); fail when the file already exists")
	cmd.Flags().StringVar(&expectedVersion, "expected-version", "", "Replace only the exact file version returned by files get")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func newAppFilesRmCommand(target *hubTarget) *cobra.Command {
	var expectedVersion string
	cmd := &cobra.Command{
		Use:     "rm <name> <path>",
		Aliases: []string{"delete"},
		Short:   "Delete a workspace file (the reconciler commits the deletion)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			if strings.TrimSpace(expectedVersion) == "" || expectedVersion == "*" {
				return fmt.Errorf("--expected-version with the version returned by files get is required")
			}
			header := http.Header{}
			header.Set("If-Match", expectedVersion)
			if err := s.doWithHeaders(ctx, http.MethodDelete, projectFileVerbURL(s, args[0], "files-content", args[1]), nil, header, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", strings.TrimSpace(args[1]))
			return err
		},
	}
	cmd.Flags().StringVar(&expectedVersion, "expected-version", "", "Delete only the exact file version returned by files get")
	return cmd
}
