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

package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// prepareEnvironment checks host prerequisites before spending a model turn.
// Network checks use the host: sandbox network permission is set separately.
func (a *Adapter) prepareEnvironment(ctx context.Context) (string, error) {
	if len(a.cfg.RegistryURLs) > 0 && !a.cfg.networkAccess {
		return "", fmt.Errorf("codex registry preflight requires sandbox_workspace_write.network_access = true in the runner Codex config")
	}
	if a.cfg.cacheScope == "" {
		return "", fmt.Errorf("codex cache scope is required")
	}
	if err := privateCacheDirectory(filepath.Clean(a.cfg.Home) + ".build-cache"); err != nil {
		return "", fmt.Errorf("codex cache preflight: %w", err)
	}
	root := a.buildCacheRoot()
	if err := privateCacheDirectory(root); err != nil {
		return "", fmt.Errorf("codex cache preflight: %w", err)
	}
	for _, name := range []string{"gomod", "go-build", "npm"} {
		if err := privateCacheDirectory(filepath.Join(root, name)); err != nil {
			return "", fmt.Errorf("codex cache preflight: %w", err)
		}
	}
	dir, err := os.MkdirTemp(root, "launch-")
	if err != nil {
		return "", fmt.Errorf("codex cache preflight: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range []string{"go", "go/bin", "tmp", "xdg"} {
		path := filepath.Join(dir, name)
		if err := os.Mkdir(path, 0700); err != nil {
			return "", fmt.Errorf("codex cache preflight: %w", err)
		}
		probe := filepath.Join(path, ".write-check")
		if err := os.WriteFile(probe, []byte("ok"), 0600); err != nil {
			return "", fmt.Errorf("codex cache preflight: %w", err)
		}
		if err := os.Remove(probe); err != nil {
			return "", fmt.Errorf("codex cache preflight: %w", err)
		}
	}
	for _, endpoint := range a.cfg.RegistryURLs {
		if err := checkRegistry(ctx, endpoint); err != nil {
			return "", err
		}
	}
	success = true
	return dir, nil
}

func checkRegistry(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("codex registry preflight requires an HTTPS URL without credentials, query or fragment")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return fmt.Errorf("codex registry preflight: invalid request")
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("codex registry preflight failed for %s: %w", u.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("codex registry preflight failed for %s: HTTP %d", u.Host, resp.StatusCode)
	}
	return nil
}

func (a *Adapter) cacheRoots() []string {
	if a.cfg.cacheDir == "" {
		return nil
	}
	return []string{a.cfg.cacheDir, filepath.Join(a.buildCacheRoot(), "gomod"), filepath.Join(a.buildCacheRoot(), "go-build"), filepath.Join(a.buildCacheRoot(), "npm")}
}

func (a *Adapter) launchEnv() []string {
	env := a.cfg.Environment.Apply(safeEnv(a.cfg.Home))
	if a.cfg.cacheDir == "" {
		return env
	}
	paths := map[string]string{
		"GOPATH": "go", "GOBIN": "go/bin", "GOMODCACHE": "gomod", "GOCACHE": "go-build", "npm_config_cache": "npm",
		"GOTMPDIR": "tmp", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp", "XDG_CACHE_HOME": "xdg",
	}
	keys := make([]string, 0, len(paths))
	reserved := map[string]bool{"GOFLAGS": true}
	for key := range paths {
		keys = append(keys, key)
		reserved[strings.ToUpper(key)] = true
	}
	sort.Strings(keys)
	filtered := make([]string, 0, len(env)+len(paths))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		override := false
		_, override = reserved[strings.ToUpper(key)]
		if !override {
			filtered = append(filtered, item)
		}
	}
	for _, key := range keys {
		name := paths[key]
		root := a.cfg.cacheDir
		if name == "gomod" || name == "go-build" || name == "npm" {
			root = a.buildCacheRoot()
		}
		filtered = append(filtered, key+"="+filepath.Join(root, name))
	}
	return append(filtered, "GOFLAGS=-modcacherw")
}

// Package-manager caches are retained per checkout and sandbox/network policy.
// Temporary files, installed executables and generic XDG state stay launch-local.
func (a *Adapter) buildCacheRoot() string {
	return filepath.Join(filepath.Clean(a.cfg.Home)+".build-cache", a.cfg.cacheScope)
}

func privateCacheDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("cache directory must be a private real directory: %s", path)
	}
	probe, err := os.CreateTemp(path, ".write-check-")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}
