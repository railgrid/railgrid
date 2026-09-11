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

package codex

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	codexConfigName     = "config.toml"
	maxCodexConfigBytes = 64 << 10
)

var errCodexConfigRejected = errors.New("codex config.toml is not an approved runner configuration")

type codexProjectConfig struct {
	TrustLevel string `toml:"trust_level"`
}

type codexConfig struct {
	Projects map[string]codexProjectConfig `toml:"projects"`
}

// validateCodexConfig validates the only project configuration that a worker
// home may contain. Multiple managed checkout entries are permitted; the
// launch path is checked separately before starting a model process.
func validateCodexConfig(home, worktreeRoot string) (bool, error) {
	path := filepath.Join(home, codexConfigName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, errCodexConfigRejected
	}
	root, err := normalizeWorktreeRoot(worktreeRoot)
	if err != nil {
		return true, errCodexConfigRejected
	}
	if info.Size() < 0 || info.Size() > maxCodexConfigBytes {
		return true, errCodexConfigRejected
	}
	f, err := os.Open(path)
	if err != nil {
		return true, errCodexConfigRejected
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return true, errCodexConfigRejected
	}
	contents, err := io.ReadAll(io.LimitReader(f, maxCodexConfigBytes+1))
	if err != nil || len(contents) > maxCodexConfigBytes {
		return true, errCodexConfigRejected
	}

	var parsed codexConfig
	metadata, err := toml.Decode(string(contents), &parsed)
	if err != nil || len(metadata.Undecoded()) != 0 || len(parsed.Projects) == 0 {
		return true, errCodexConfigRejected
	}
	for projectPath, project := range parsed.Projects {
		if project.TrustLevel != "trusted" && project.TrustLevel != "untrusted" {
			return true, errCodexConfigRejected
		}
		if err := validateTrustProjectPath(root, projectPath); err != nil {
			return true, errCodexConfigRejected
		}
	}
	return true, nil
}

func normalizeWorktreeRoot(worktreeRoot string) (string, error) {
	if strings.TrimSpace(worktreeRoot) == "" || !filepath.IsAbs(worktreeRoot) {
		return "", errCodexConfigRejected
	}
	root := filepath.Clean(worktreeRoot)
	info, err := os.Lstat(root)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errCodexConfigRejected
		}
	} else {
		return "", errCodexConfigRejected
	}
	return root, nil
}

func validateTrustProjectPath(root, projectPath string) error {
	if !filepath.IsAbs(projectPath) || filepath.Clean(projectPath) != projectPath {
		return errCodexConfigRejected
	}
	rel, err := filepath.Rel(root, projectPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errCodexConfigRejected
	}
	parts := splitPath(rel)
	if len(parts) != 2 {
		return errCodexConfigRejected
	}
	return validateNoSymlinkComponents(root, projectPath)
}

func validateManagedWorktree(root, workdir string) error {
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return errCodexConfigRejected
	}
	if err := validateTrustProjectPath(root, workdir); err != nil {
		return err
	}
	info, err := os.Lstat(workdir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errCodexConfigRejected
	}
	return nil
}

func validateNoSymlinkComponents(root, candidate string) error {
	rootInfo, err := os.Lstat(root)
	if err == nil {
		if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			return errCodexConfigRejected
		}
	} else {
		return errCodexConfigRejected
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return errCodexConfigRejected
	}
	current := root
	for _, part := range splitPath(rel) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errCodexConfigRejected
		}
	}
	return nil
}

func splitPath(path string) []string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == rune(filepath.Separator) })
	return parts
}

func (a *Adapter) validateLaunchConfiguration(workdir string) error {
	present, err := validateCodexConfig(a.cfg.Home, a.cfg.WorktreeRoot)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	root, err := normalizeWorktreeRoot(a.cfg.WorktreeRoot)
	if err != nil {
		return errCodexConfigRejected
	}
	if err := validateManagedWorktree(root, workdir); err != nil {
		return errCodexConfigRejected
	}
	return rejectLocalCodexConfiguration(root, workdir)
}

func rejectLocalCodexConfiguration(root, workdir string) error {
	current := workdir
	for {
		candidate := filepath.Join(current, ".codex")
		if _, err := os.Lstat(candidate); err == nil {
			return errCodexConfigRejected
		} else if !errors.Is(err, os.ErrNotExist) {
			return errCodexConfigRejected
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return errCodexConfigRejected
		}
		current = parent
	}
}
