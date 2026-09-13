// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-linear/internal/actionapi"
)

func TestActionInputRejectsCrossActionFieldsBeforeCredentials(t *testing.T) {
	for _, input := range []actionapi.Input{
		{Connection: "other"}, {TeamID: "other"}, {Action: "createIssue"},
		{Body: "hidden write"}, {IssueID: "other"}, {After: strings.Repeat("a", 4097)}, {First: 51},
	} {
		// No authority or credential resolver exists: validation must stop first.
		_, err := (Server{}).Action(context.Background(), httptest.NewRequest("POST", "/", nil), "team", "states", ActionRequest{Input: input}, false)
		if !errors.Is(err, errActionInput) {
			t.Fatalf("input %#v returned %v", input, err)
		}
	}
	if !validActionInput("states", actionapi.Input{First: 50, After: "cursor"}) {
		t.Fatal("valid bounded page rejected")
	}
	for _, action := range []string{"issue", "comments", "replies", "create_issue", "update_issue", "add_comment"} {
		if validActionInput(action, actionapi.Input{}) {
			t.Fatalf("%s accepted missing required input", action)
		}
	}
}
