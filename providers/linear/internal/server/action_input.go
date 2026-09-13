// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/faroshq/provider-linear/internal/actionapi"
)

var errActionInput = errors.New("invalid action input")
var errActionForbidden = errors.New("action forbidden")
var errActionConflict = errors.New("action identity conflict")

func validActionInput(action string, input actionapi.Input) bool {
	fields := map[string]string{
		"states": "first after", "issues": "first after query since",
		"issue": "issueID", "comments": "issueID first after",
		"replies":      "issueID commentID first after",
		"create_issue": "title description stateID",
		"update_issue": "issueID title description stateID",
		"add_comment":  "issueID body",
	}
	allowed, ok := fields[action]
	if !ok {
		return false
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return false
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for field := range values {
		if !strings.Contains(" "+allowed+" ", " "+field+" ") {
			return false
		}
	}
	if input.First < 0 || input.First > 50 || len(input.After) > 4096 || len(input.Query) > 1000 || len(input.IssueID) > 255 || len(input.CommentID) > 255 || len(input.Body) > 16000 {
		return false
	}
	if input.Title != nil && len(*input.Title) > 255 || input.Description != nil && len(*input.Description) > 16000 || input.StateID != nil && len(*input.StateID) > 255 {
		return false
	}
	if action == "create_issue" && (input.Title == nil || strings.TrimSpace(*input.Title) == "") {
		return false
	}
	if action == "issue" || action == "comments" || action == "replies" || action == "update_issue" || action == "add_comment" {
		if strings.TrimSpace(input.IssueID) == "" {
			return false
		}
	}
	if action == "replies" && strings.TrimSpace(input.CommentID) == "" || action == "add_comment" && strings.TrimSpace(input.Body) == "" {
		return false
	}
	return true
}
