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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const stateSessionsDir = "exec-sessions"

// prepareCoordinatorState creates coordinator-owned durable state outside the
// application workspace. No application or executor container needs this path.
func prepareCoordinatorState(stateDir, workspace string) (string, error) {
	stateDir = filepath.Clean(stateDir)
	workspace = filepath.Clean(workspace)
	if stateDir == "." || stateDir == "" {
		return "", errors.New("FAROS_DEV_STATE_DIR is required")
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve coordinator state directory: %w", err)
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if absState == absWorkspace || isWithin(absWorkspace, absState) || isWithin(absState, absWorkspace) {
		return "", errors.New("FAROS_DEV_STATE_DIR must be independent of the workspace")
	}
	if err := rejectRootSymlink(absState); err != nil {
		return "", fmt.Errorf("coordinator state directory: %w", err)
	}
	rootInfo, err := os.Lstat(absState)
	if err != nil {
		return "", fmt.Errorf("inspect coordinator state mount: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("coordinator state mount %s is not a real directory", absState)
	}
	// Kubernetes owns the PVC mount root. With fsGroup it is commonly
	// root:<fsGroup> and 0770; the non-root coordinator may create children but
	// cannot chmod the mount itself. Validate writability and preserve its mode.
	if rootInfo.Mode().Perm()&0o220 == 0 {
		return "", fmt.Errorf("coordinator state mount %s is not owner/group writable", absState)
	}
	sessionsDir := filepath.Join(absState, stateSessionsDir)
	info, statErr := os.Lstat(sessionsDir)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.Mkdir(sessionsDir, 0o700); err != nil {
			return "", fmt.Errorf("create coordinator sessions directory: %w", err)
		}
	case statErr != nil:
		return "", fmt.Errorf("inspect coordinator sessions directory: %w", statErr)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("coordinator sessions path %s is not a real directory", sessionsDir)
	}
	if err := os.Chmod(sessionsDir, 0o700); err != nil {
		return "", fmt.Errorf("protect coordinator sessions directory: %w", err)
	}
	return absState, nil
}

func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
