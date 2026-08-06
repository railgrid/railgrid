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

package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

// NewActivationSource wraps a system source with activation policy owned by a
// single project. The source itself remains read-only; only Package.Enabled is
// overlaid before catalog loading computes entry and catalog digests. When
// disableAll is true (for example, malformed project metadata), every listed
// package is disabled so callers fail closed.
func NewActivationSource(source Source, activations map[string]Activation, disableAll bool) (Source, error) {
	if source == nil {
		return nil, errors.New("skill source cannot be nil")
	}
	if source.Scope() != ScopeSystem {
		return nil, fmt.Errorf("activation source requires a system source, got %q", source.Scope())
	}
	if len(activations) > DefaultMaxPackages*4 {
		return nil, errors.New("skill activation metadata exceeds its bound")
	}
	copyActivations := make(map[string]Activation, len(activations))
	for rawPath, activation := range activations {
		clean, err := cleanPublicPackagePath(rawPath)
		if err != nil || clean != rawPath || len([]byte(clean)) > workspace.MaxProjectPathBytes {
			return nil, errors.New("skill activation metadata contains an invalid package identity")
		}
		if len([]byte(activation.Version)) > 128 || len([]byte(activation.Digest)) > 128 {
			return nil, errors.New("skill activation metadata contains oversized provenance")
		}
		copyActivations[clean] = activation
	}
	return &activationSource{source: source, activations: copyActivations, disableAll: disableAll}, nil
}

type activationSource struct {
	source      Source
	activations map[string]Activation
	disableAll  bool
}

func (s *activationSource) Scope() Scope { return s.source.Scope() }

func (s *activationSource) List(ctx context.Context, maxPackages int) (PackageList, error) {
	list, err := s.source.List(ctx, maxPackages)
	if err != nil {
		return PackageList{}, err
	}
	for index := range list.Packages {
		packageEntry := &list.Packages[index]
		if s.disableAll {
			packageEntry.Enabled = false
			packageEntry.EnabledSet = true
			continue
		}
		activation, ok := s.activations[packageEntry.Path]
		if !ok {
			continue
		}
		if !activationMatchesPackageVersion(*packageEntry, activation) {
			packageEntry.Enabled = false
			packageEntry.EnabledSet = true
			list.Warnings = appendBoundedWarning(list.Warnings, Warning{Scope: ScopeSystem, PackagePath: packageEntry.Path, Code: "activation_stale", Message: "skill activation metadata is stale; package is disabled"}, DefaultMaxWarnings)
			continue
		}
		packageEntry.Enabled = activation.Enabled
		packageEntry.EnabledSet = true
	}
	if s.disableAll {
		list.Warnings = appendBoundedWarning(list.Warnings, Warning{Scope: ScopeSystem, Code: "activation_metadata_invalid", Message: "skill activation metadata is invalid; packages are disabled"}, DefaultMaxWarnings)
	}
	return list, nil
}

func (s *activationSource) ReadResource(ctx context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	return s.source.ReadResource(ctx, packagePath, resourcePath, opts)
}

func activationMatchesPackageVersion(packageEntry Package, activation Activation) bool {
	expected := strings.TrimSpace(activation.Version)
	if expected == "" {
		return true
	}
	if version := strings.TrimSpace(packageEntry.Version); version != "" {
		return expected == version
	}
	parsed, err := ParseSkill(packageEntry.SkillContent, DefaultLimits())
	if err != nil {
		return false
	}
	return expected == digestBytes([]byte(parsed.Content))
}
