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

package backend

import "regexp"

// FinalResponseArtifactName identifies the agent-authored public PR report.
const FinalResponseArtifactName = "final-response.md"

var publicReportSecrets = regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_]+|github_pat_[a-z0-9_]+|bearer[ \t]+[a-z0-9._~+/=-]+)`)

// PublicReport removes recognizable credentials. It is not a general secret
// detector, and cannot sanitize image pixels; see Code attachment documentation.
func PublicReport(report string) string {
	return publicReportSecrets.ReplaceAllString(report, "[REDACTED]")
}
