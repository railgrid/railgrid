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

// Package runner provides a durable, authenticated local execution service.
package runner

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	defaultMaxEvents        = 256
	defaultMaxEventBytes    = 64 << 10
	defaultMaxBodyBytes     = 2 << 20
	defaultMaxArtifactSize  = 32 << 20
	gitResultCapability     = "git-result-v1"
	gitFetchCapability      = "git-fetch-v1"
	clarificationCapability = "clarification-v1"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// RepositoryConfig enrolls one local Git source. The source is read-only from
// the runner's point of view; attempts are cloned into task-owned worktrees.
// FetchRemoteURL is operator-only enrollment data; it is never accepted from
// a start request or exposed as a runner protocol field.
type RepositoryConfig struct {
	Source         string `json:"source"`
	BaseCommit     string `json:"baseCommit,omitempty"`
	FetchRemoteURL string `json:"fetchRemoteURL,omitempty"`
}

// ResourceConfig names a preconfigured shared resource. Capacity is a count
// of simultaneous reservations; it does not cause the runner to provision the
// resource.
type ResourceConfig struct {
	Kind     string            `json:"kind,omitempty"`
	Capacity int               `json:"capacity,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Config contains local runner identity, enrollment, persistence, and
// capability settings. It is intentionally independent of the Codex adapter.
type Config struct {
	ProtocolVersion string                      `json:"protocolVersion,omitempty"`
	RunnerID        string                      `json:"runnerID"`
	Version         string                      `json:"version,omitempty"`
	Listen          string                      `json:"listen,omitempty"`
	StateDir        string                      `json:"stateDir"`
	TokenFile       string                      `json:"tokenFile"`
	Token           string                      `json:"-"`
	Toolchains      []string                    `json:"toolchains,omitempty"`
	Environment     []string                    `json:"environment,omitempty"`
	Verification    []string                    `json:"verificationCapabilities,omitempty"`
	MaximumCapacity int                         `json:"maximumCapacity,omitempty"`
	MaxEvents       int                         `json:"maxEvents,omitempty"`
	MaxEventBytes   int                         `json:"maxEventBytes,omitempty"`
	MaxBodyBytes    int                         `json:"maxBodyBytes,omitempty"`
	MaxArtifactSize int64                       `json:"maxArtifactSize,omitempty"`
	Repositories    map[string]RepositoryConfig `json:"repositories,omitempty"`
	Resources       map[string]ResourceConfig   `json:"resources,omitempty"`
}

// LoadConfig reads a JSON runner configuration. A missing path is equivalent
// to an empty configuration and is useful for callers that supply Config
// directly; the CLI requires a path when --config is set.
func LoadConfig(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read runner config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode runner config: %w", err)
	}
	return cfg, nil
}

func (c *Config) applyDefaults() error {
	if c.ProtocolVersion == "" {
		c.ProtocolVersion = ProtocolVersion
	}
	if c.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported runner protocol version %q", c.ProtocolVersion)
	}
	if c.RunnerID == "" {
		c.RunnerID = "faros-runner-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	if c.StateDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("resolve default runner state directory: %w", err)
		}
		c.StateDir = filepath.Join(base, "faros-runner")
	}
	abs, err := filepath.Abs(c.StateDir)
	if err != nil {
		return fmt.Errorf("resolve runner state directory: %w", err)
	}
	c.StateDir = abs
	if c.MaximumCapacity <= 0 {
		c.MaximumCapacity = 1
	} else if c.MaximumCapacity > 1 {
		return errors.New("runner maximumCapacity must be 1 for the single-execution runner")
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = defaultMaxEvents
	}
	if c.MaxEventBytes <= 0 {
		c.MaxEventBytes = defaultMaxEventBytes
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = defaultMaxBodyBytes
	}
	if c.MaxArtifactSize <= 0 {
		c.MaxArtifactSize = defaultMaxArtifactSize
	}
	if c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return fmt.Errorf("read runner token file: %w", err)
		}
		c.Token = strings.TrimSpace(string(b))
	}
	if c.Token == "" {
		return errors.New("runner bearer token is empty")
	}
	if len(c.Token) > 4096 || strings.ContainsAny(c.Token, "\r\n \t") {
		return errors.New("runner bearer token contains invalid whitespace or is too long")
	}
	if !identifierPattern.MatchString(c.RunnerID) {
		return fmt.Errorf("runner ID %q is invalid", c.RunnerID)
	}
	if !isLoopbackListenAddress(c.Listen) {
		return fmt.Errorf("runner listen address %q is not loopback-only", c.Listen)
	}
	for id, repo := range c.Repositories {
		if !identifierPattern.MatchString(id) {
			return fmt.Errorf("repository ID %q is invalid", id)
		}
		if strings.TrimSpace(repo.Source) == "" {
			return fmt.Errorf("repository %q has an empty source", id)
		}
		if abs, err := filepath.Abs(repo.Source); err == nil {
			repo.Source = abs
		} else {
			return fmt.Errorf("resolve repository %q source: %w", id, err)
		}
		if strings.TrimSpace(repo.FetchRemoteURL) != "" {
			remote, err := validateFetchRemoteURL(repo.FetchRemoteURL)
			if err != nil {
				return fmt.Errorf("repository %q fetch remote URL: %w", id, err)
			}
			repo.FetchRemoteURL = remote
		}
		c.Repositories[id] = repo
	}
	for name, resource := range c.Resources {
		if !identifierPattern.MatchString(name) {
			return fmt.Errorf("resource name %q is invalid", name)
		}
		if resource.Capacity < 0 {
			return fmt.Errorf("resource %q has negative capacity", name)
		}
		if resource.Capacity == 0 {
			resource.Capacity = 1
			c.Resources[name] = resource
		}
	}
	return nil
}

func hasFetchRemote(repositories map[string]RepositoryConfig) bool {
	for _, repository := range repositories {
		if strings.TrimSpace(repository.FetchRemoteURL) != "" {
			return true
		}
	}
	return false
}

func isLoopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func tokenEqual(expected, presented string) bool {
	if len(expected) != len(presented) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}
