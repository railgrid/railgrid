/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"strings"
	"testing"
)

func TestCommitMessageDescribesChanges(t *testing.T) {
	// Single file → names it.
	if got := commitMessage([]string{"web/src/App.jsx"}, nil); !strings.HasPrefix(got, "Update web/src/App.jsx") {
		t.Fatalf("single-file subject = %q", got)
	}
	// Single deletion → names it.
	if got := commitMessage(nil, []string{"api/old.mjs"}); !strings.HasPrefix(got, "Delete api/old.mjs") {
		t.Fatalf("single-delete subject = %q", got)
	}
	// Many files → count + top-level areas + body lists paths.
	got := commitMessage([]string{"web/a.jsx", "web/b.jsx", "api/s.mjs"}, []string{"api/old.mjs"})
	subject := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(subject, "3 files") || !strings.Contains(subject, "delete 1 file") {
		t.Fatalf("multi subject = %q", subject)
	}
	if !strings.Contains(subject, "web") || !strings.Contains(subject, "api") {
		t.Fatalf("multi subject missing areas: %q", subject)
	}
	if !strings.Contains(got, "- web/a.jsx") || !strings.Contains(got, "- delete api/old.mjs") {
		t.Fatalf("body missing paths: %q", got)
	}
	// Not the old useless message.
	if strings.Contains(got, "sync workspace") {
		t.Fatalf("still using the generic message: %q", got)
	}
}
