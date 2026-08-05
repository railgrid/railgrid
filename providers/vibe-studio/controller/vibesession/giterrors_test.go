// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import (
	"errors"
	"strings"
	"testing"
)

func TestExplainGitErrorNamesTheWorkflowScope(t *testing.T) {
	err := errors.New("PUT https://api.github.com/repos/o/r/contents: 403 refusing to allow an OAuth App " +
		"to create or update workflow `.github/workflows/build.yaml` without `workflow` scope")
	got := explainGitError(err)
	if !strings.Contains(got, "workflow` scope") || !strings.Contains(got, "Reconnect") {
		t.Errorf("explainGitError = %q, want a reconnect instruction naming the scope", got)
	}
	if !strings.Contains(got, err.Error()) {
		t.Error("the git host's own words should be kept for diagnosis")
	}
}

func TestExplainGitErrorPassesThroughOtherFailures(t *testing.T) {
	err := errors.New("connection reset by peer")
	if got := explainGitError(err); got != err.Error() {
		t.Errorf("explainGitError = %q, want the error unchanged", got)
	}
	if got := explainGitError(nil); got != "" {
		t.Errorf("explainGitError(nil) = %q, want empty", got)
	}
}
