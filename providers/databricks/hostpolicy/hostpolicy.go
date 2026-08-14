// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package hostpolicy contains the shared Databricks workspace-host allowlist.
// Keep this policy in one place so validation and metadata discovery cannot
// drift on whether configured suffixes replace or extend the defaults.
package hostpolicy

import (
	"net"
	"os"
	"strings"
)

const AllowedHostSuffixesEnv = "DATABRICKS_ALLOWED_HOST_SUFFIXES"

var defaultWorkspaceHostSuffixes = []string{
	"cloud.databricks.com",
	"gcp.databricks.com",
	"azuredatabricks.net",
}

// WorkspaceHostSuffixes returns the additive default-plus-configured suffix
// policy. An empty configured slice reads the optional environment override;
// defaults are always retained. Invalid and IP-address suffixes are ignored.
func WorkspaceHostSuffixes(configured []string) []string {
	if len(configured) == 0 {
		configured = splitCSV(os.Getenv(AllowedHostSuffixesEnv))
	}

	out := make([]string, 0, len(defaultWorkspaceHostSuffixes)+len(configured))
	seen := make(map[string]struct{}, cap(out))
	for _, suffix := range append(append([]string(nil), defaultWorkspaceHostSuffixes...), configured...) {
		suffix = normalizeSuffix(suffix)
		if suffix == "" || net.ParseIP(suffix) != nil {
			continue
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		out = append(out, suffix)
	}
	return out
}

// AllowedWorkspaceHost reports whether hostname is an exact allowlisted host
// or a subdomain of an allowlisted suffix. IP addresses never satisfy the
// workspace suffix policy; loopback development exceptions are handled by the
// caller before invoking this function.
func AllowedWorkspaceHost(hostname string, configured []string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if hostname == "" || net.ParseIP(strings.Trim(hostname, "[]")) != nil {
		return false
	}
	for _, suffix := range WorkspaceHostSuffixes(configured) {
		if hostname == suffix || strings.HasSuffix(hostname, "."+suffix) {
			return true
		}
	}
	return false
}

func normalizeSuffix(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Trim(value, "[]")
	value = strings.Trim(value, ".")
	return value
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
