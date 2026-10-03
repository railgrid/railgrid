// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package runner

import (
	"strings"
	"testing"
)

// The coordinator may name the snapshot commit's subject; the runner stamps
// exactly that, or the canonical message when none was named. What it will
// not stamp is anything that is not one bounded, printable line — Code would
// refuse the commit, so the request is refused first.
func TestCommitMessageIsOneBoundedLineOrCanonical(t *testing.T) {
	if got := commitMessageFor(StartRequest{}); got != gitResultMessage {
		t.Fatalf("no subject stamped %q, want the canonical message", got)
	}
	if got := commitMessageFor(StartRequest{CommitMessage: "  ENG-4: test  "}); got != "ENG-4: test" {
		t.Fatalf("subject stamped %q, want it trimmed", got)
	}
	for _, ok := range []string{"", "ENG-4: test", "Address review on #7 (revision 2)", strings.Repeat("x", 200)} {
		if err := validateCommitMessage(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
	for _, bad := range []string{strings.Repeat("x", 201), "subject\n\nbody", "tab\there", "bell\x07"} {
		if err := validateCommitMessage(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
