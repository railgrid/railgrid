// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import "testing"

func TestPostsRunOutput(t *testing.T) {
	tests := []struct {
		name          string
		notifications *AgentNotifications
		wantSucceeded bool
		wantFailed    bool
	}{
		{name: "unset keeps posting everything", notifications: nil, wantSucceeded: true, wantFailed: true},
		{name: "empty runOutput is Always", notifications: &AgentNotifications{}, wantSucceeded: true, wantFailed: true},
		{name: "Always", notifications: &AgentNotifications{RunOutput: AgentRunOutputAlways}, wantSucceeded: true, wantFailed: true},
		{name: "OnFailure", notifications: &AgentNotifications{RunOutput: AgentRunOutputOnFailure}, wantSucceeded: false, wantFailed: true},
		{name: "Never", notifications: &AgentNotifications{RunOutput: AgentRunOutputNever}, wantSucceeded: false, wantFailed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := AgentSpec{Notifications: tt.notifications}
			if got := spec.PostsRunOutput(false); got != tt.wantSucceeded {
				t.Errorf("PostsRunOutput(succeeded) = %t, want %t", got, tt.wantSucceeded)
			}
			if got := spec.PostsRunOutput(true); got != tt.wantFailed {
				t.Errorf("PostsRunOutput(failed) = %t, want %t", got, tt.wantFailed)
			}
		})
	}
}
