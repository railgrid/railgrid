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

import (
	"strings"
	"testing"
)

func TestPublicReportRedactsKnownCredentials(t *testing.T) {
	input := "Verified UI. ghp_secret github_pat_secret Authorization: Bearer abc.def-123 ![UI](artifacts/screenshots/a.png)"
	output := PublicReport(input)
	if strings.Contains(output, "secret") || strings.Contains(output, "abc.def") || !strings.Contains(output, "![UI](artifacts/screenshots/a.png)") {
		t.Fatal("public report not sanitized")
	}
}
