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

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/faroshq/provider-app-studio/workspace"
)

func previewProjectWorkspacePatch(ctx context.Context, store *workspace.FileStore, scope workspace.Scope, opts workspace.PatchOptions) (string, string, error) {
	if strings.TrimSpace(opts.OldText) == "" {
		return "", "", errors.New("oldText is required")
	}
	if len([]byte(opts.OldText))+len([]byte(opts.NewText)) > workspace.MaxPatchBytes {
		return "", "", fmt.Errorf("patch text is too large: %d > %d bytes", len([]byte(opts.OldText))+len([]byte(opts.NewText)), workspace.MaxPatchBytes)
	}
	if !utf8.ValidString(opts.OldText) || !utf8.ValidString(opts.NewText) || strings.Contains(opts.OldText, "\x00") || strings.Contains(opts.NewText, "\x00") {
		return "", "", errors.New("patch text must be UTF-8 text without NUL bytes")
	}
	read, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: opts.Path, MaxBytes: workspace.MaxWriteBytes})
	if err != nil {
		return "", "", err
	}
	if read.Binary {
		return "", "", fmt.Errorf("file %q is binary", read.Path)
	}
	if read.Truncated {
		return "", "", fmt.Errorf("file %q is too large to patch", read.Path)
	}
	count := strings.Count(read.Content, opts.OldText)
	if count == 0 {
		return "", "", fmt.Errorf("oldText was not found in %q", read.Path)
	}
	if count > 1 && !opts.ReplaceAll {
		return "", "", fmt.Errorf("oldText matched %d times in %q; set replaceAll to true or provide a more specific oldText", count, read.Path)
	}
	next := strings.Replace(read.Content, opts.OldText, opts.NewText, 1)
	if opts.ReplaceAll {
		next = strings.ReplaceAll(read.Content, opts.OldText, opts.NewText)
	}
	return read.Path, next, nil
}
