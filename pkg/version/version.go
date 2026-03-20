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

// Package version holds build-time version information injected via ldflags.
package version

// These vars are set via -ldflags at build time. See Makefile LDFLAGS target.
var (
	// Version is the semantic version of the binary (e.g. "v0.0.28").
	Version = "dev"
	// GitCommit is the short git commit SHA.
	GitCommit = "unknown"
	// BuildDate is the RFC3339 build timestamp.
	BuildDate = "unknown"
)

// Get returns the current binary version string.
func Get() string { return Version }
