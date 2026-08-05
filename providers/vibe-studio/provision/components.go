// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package provision

import (
	"fmt"
	"strings"
)

// Component vocabulary. Templates follow the ONE NAME RULE — a component's
// name IS its workspace directory — but models still reach for the directory,
// so these helpers state the distinction once and forgive the confusion.

// ComponentsText describes the components for a system prompt.
func ComponentsText(components map[string]string) string {
	if len(components) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(components))
	for _, name := range sortedKeys(components) {
		parts = append(parts, fmt.Sprintf("component %q serves the files under %s/", name, components[name]))
	}
	return strings.Join(parts, "; ") +
		". Tools that take a component want the NAME (" + ComponentNames(components) + "), never the directory."
}

// ComponentNames lists the valid component names, comma-separated.
func ComponentNames(components map[string]string) string {
	return strings.Join(sortedKeys(components), ", ")
}

// ComponentEnum constrains a tool parameter to the valid names.
func ComponentEnum(components map[string]string) []string {
	if len(components) == 0 {
		return nil
	}
	return sortedKeys(components)
}

// ResolveComponent accepts a component name and, forgivingly, its workspace
// path; anything else returns an error naming the valid options, which the
// engine feeds back as an observation so the model can correct itself.
func ResolveComponent(components map[string]string, want string) (string, error) {
	want = strings.Trim(strings.TrimSpace(want), "/")
	if want == "" && len(components) == 1 {
		return sortedKeys(components)[0], nil
	}
	if _, ok := components[want]; ok {
		return want, nil
	}
	for name, wp := range components {
		if strings.Trim(wp, "/") == want {
			return name, nil
		}
	}
	return "", fmt.Errorf("unknown component %q; valid components are: %s", want, ComponentNames(components))
}

// Tail truncates long output from the end (logs, error bodies).
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
