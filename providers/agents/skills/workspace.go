// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package skills adapts the workspace library to the shared skill catalog.
package skills

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	sdk "github.com/railgrid/provider-sdk/skills"
)

const MaxPackageBytes = 512 << 10

// Validate applies shared parsing and additional KRM package bounds. It runs
// even for objects created directly through kcp rather than the workbench.
func Validate(skill agents.Skill) error {
	if _, err := sdk.ValidatePackagePath(skill.Name); err != nil {
		return err
	}
	if _, err := sdk.ParseSkill([]byte(skill.Spec.Document), sdk.DefaultLimits()); err != nil {
		return err
	}
	if len(skill.Spec.Resources) > 64 {
		return errors.New("skill has more than 64 supporting resources")
	}
	total := len(skill.Spec.Document)
	seen := map[string]bool{}
	for _, resource := range skill.Spec.Resources {
		clean, err := sdk.ValidateResourcePath(resource.Name)
		if err != nil || clean != resource.Name || clean == "SKILL.md" || strings.HasSuffix(clean, "/SKILL.md") {
			return errors.New("skill resource path is invalid")
		}
		if seen[clean] {
			return errors.New("skill resource paths must be unique")
		}
		seen[clean] = true
		if len(resource.Content) > sdk.DefaultMaxResourceBytes || !utf8.ValidString(resource.Content) || strings.ContainsRune(resource.Content, 0) {
			return errors.New("skill resource content is invalid or oversized")
		}
		total += len(resource.Content)
	}
	if total > MaxPackageBytes {
		return fmt.Errorf("skill package exceeds %d bytes", MaxPackageBytes)
	}
	return nil
}

type workspaceSource struct {
	packages []sdk.Package
	data     map[string]map[string][]byte
	warnings []sdk.Warning
}

// NewWorkspaceSource copies valid packages, retaining sanitized warnings for
// invalid siblings so one bad custom package cannot disable the whole catalog.
func NewWorkspaceSource(items []agents.Skill) sdk.Source {
	s := &workspaceSource{data: map[string]map[string][]byte{}}
	sorted := append([]agents.Skill(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, skill := range sorted {
		if err := Validate(skill); err != nil {
			s.warnings = sdk.AppendWarning(s.warnings, sdk.Warning{Scope: sdk.ScopeWorkspace, PackagePath: skill.Name, Code: "invalid_package", Message: "skill package is invalid or exceeds its bounds"}, sdk.DefaultMaxWarnings)
			continue
		}
		p := sdk.Package{Path: skill.Name, SkillContent: []byte(skill.Spec.Document)}
		data := map[string][]byte{}
		for _, r := range skill.Spec.Resources {
			p.Resources = append(p.Resources, sdk.ResourceRef{Path: r.Name, Size: int64(len(r.Content))})
			data[r.Name] = []byte(r.Content)
		}
		s.packages = append(s.packages, p)
		s.data[skill.Name] = data
	}
	return s
}

func (s *workspaceSource) Scope() sdk.Scope { return sdk.ScopeWorkspace }
func (s *workspaceSource) List(ctx context.Context, max int) (sdk.PackageList, error) {
	if err := ctx.Err(); err != nil {
		return sdk.PackageList{}, err
	}
	if max <= 0 {
		max = sdk.DefaultMaxPackages
	}
	n := min(max, len(s.packages))
	return sdk.PackageList{Packages: append([]sdk.Package(nil), s.packages[:n]...), Warnings: append([]sdk.Warning(nil), s.warnings...), Truncated: n < len(s.packages)}, nil
}
func (s *workspaceSource) ReadResource(ctx context.Context, p, r string, opts sdk.ResourceReadOptions) (sdk.ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return sdk.ResourceReadResult{}, err
	}
	data, ok := s.data[p][r]
	if !ok {
		return sdk.ResourceReadResult{}, sdk.ErrResourceNotFound
	}
	opts, err := sdk.BoundReadOptions(opts, sdk.DefaultMaxResourceRead)
	if err != nil {
		return sdk.ResourceReadResult{}, err
	}
	start := min(opts.Offset, int64(len(data)))
	end := min(start+int64(opts.Limit), int64(len(data)))
	out := sdk.ResourceReadResult{Path: r, Content: append([]byte(nil), data[start:end]...), Size: int64(len(data)), Offset: start, Truncated: end < int64(len(data))}
	if out.Truncated {
		out.NextOffset = end
	}
	return out, nil
}

type activationSource struct {
	sdk.Source
	enabled map[string]bool
}

func (s activationSource) List(ctx context.Context, max int) (sdk.PackageList, error) {
	list, err := s.Source.List(ctx, max)
	if err != nil {
		return list, err
	}
	for i := range list.Packages {
		list.Packages[i].EnabledSet = true
		list.Packages[i].Enabled = s.enabled[list.Packages[i].Path]
	}
	return list, nil
}

// Build preserves every library entry for management while applying explicit
// per-agent opt-in before computing immutable catalog and entry digests.
func Build(ctx context.Context, sources []sdk.Source, refs []agents.AgentSkillReference) (sdk.Snapshot, error) {
	all, err := sdk.Build(ctx, sdk.CatalogOptions{Sources: sources})
	if err != nil {
		return sdk.Snapshot{}, err
	}
	wanted := map[string]bool{}
	for _, ref := range refs {
		wanted[ref.Name] = true
	}
	enabled := map[sdk.Scope]map[string]bool{}
	for _, entry := range all.Entries {
		if wanted[entry.QualifiedName] {
			if enabled[entry.Scope] == nil {
				enabled[entry.Scope] = map[string]bool{}
			}
			enabled[entry.Scope][entry.PackagePath] = true
		}
	}
	filtered := make([]sdk.Source, 0, len(sources))
	for _, source := range sources {
		filtered = append(filtered, activationSource{Source: source, enabled: enabled[source.Scope()]})
	}
	snapshot, err := sdk.Build(ctx, sdk.CatalogOptions{Sources: filtered})
	if err != nil {
		return sdk.Snapshot{}, err
	}
	for _, ref := range refs {
		if _, ok := snapshot.Find(ref.Name); !ok {
			snapshot.Warnings = sdk.AppendWarning(snapshot.Warnings, sdk.Warning{Scope: sdk.ScopeWorkspace, Code: "activation_unavailable", Message: "an enabled skill is no longer available; review this agent’s skills"}, sdk.DefaultMaxWarnings)
		}
	}
	return snapshot, nil
}
