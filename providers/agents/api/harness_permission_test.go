// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

// A harness PERMISSION park names a call, so it must be shown as an approval —
// the card with Approve and Deny — rather than as a question with a text box.
func TestPendingForAHarnessPermissionParkIsAnApproval(t *testing.T) {
	pending := pendingFor("inbox-1", &backend.Parked{Tool: "Bash", Args: `{"command":"ls"}`})
	if pending == nil || pending.Kind != string(store.InboxKindApproval) {
		t.Fatalf("pending = %+v, want an approval", pending)
	}
	if pending.Tool != "Bash" || pending.Args != `{"command":"ls"}` {
		t.Fatalf("pending = %+v, want the call the approval authorizes", pending)
	}
	if pending.Question != "" {
		t.Fatalf("pending question = %q; a verdict is not an answer", pending.Question)
	}

	// The other park is still a question.
	question := pendingFor("inbox-2", &backend.Parked{Question: "which branch?"})
	if question == nil || question.Kind != string(store.InboxKindQuestion) {
		t.Fatalf("pending = %+v, want a question", question)
	}
}

// An approval may only be GRANTED when its arguments read back as an object
// (approvalDisclosureAvailable). A harness input is bounded on the way here, so
// a large one arrives truncated and would otherwise become deny-only.
func TestApprovableArgsKeepsTheDisclosureGrantable(t *testing.T) {
	for name, args := range map[string]string{
		"already an object": `{"command":"ls","description":"list"}`,
		"truncated json":    `{"command":"a very long comm`,
		"not json at all":   `rm -rf /`,
		"a json array":      `["not","an","object"]`,
		"empty":             "",
	} {
		t.Run(name, func(t *testing.T) {
			normalized := approvableArgs(args)
			item := store.InboxItem{
				Kind:    store.InboxKindApproval,
				Payload: map[string]any{"tool": "Bash", "args": normalized},
			}
			if !approvalDisclosureAvailable(item) {
				t.Fatalf("approvableArgs(%q) = %q, which cannot be approved", args, normalized)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal([]byte(normalized), &object); err != nil {
				t.Fatalf("normalized args are not an object: %v", err)
			}
		})
	}
	// An input that is already an object is passed through untouched: the
	// person is shown what the harness actually proposed.
	if got := approvableArgs(`{"command":"ls"}`); got != `{"command":"ls"}` {
		t.Fatalf("approvableArgs rewrote a usable disclosure: %q", got)
	}
	// Anything else is wrapped rather than discarded, so the text survives.
	wrapped := approvableArgs("rm -rf /")
	if !strings.Contains(wrapped, "rm -rf /") {
		t.Fatalf("wrapping lost the call: %q", wrapped)
	}
}
