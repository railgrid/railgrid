/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"strings"
	"testing"
)

func TestValidateProjectComponentSyncResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{
			name:       "success",
			statusCode: 200,
			body:       "{\"phase\":\"Synced\",\"reloadRuns\":[\"npm install\"]}",
		},
		{
			name:       "reload failure in successful HTTP response",
			statusCode: 200,
			body:       "{\"phase\":\"Synced\",\"reloadError\":\"reload command npm install: exit status 1\"}",
			wantError:  "component server dependency reload failed: reload command npm install: exit status 1",
		},
		{
			name:       "upstream failure",
			statusCode: 502,
			body:       "runtime unavailable",
			wantError:  "component server sync returned 502: runtime unavailable",
		},
		{
			name:       "invalid success response",
			statusCode: 200,
			body:       "not-json",
			wantError:  "component server sync returned an invalid response",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProjectComponentSyncResponse("server", tc.statusCode, []byte(tc.body))
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("validate sync response: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("validate sync response error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateProjectComponentSyncResponseBoundsReloadFailure(t *testing.T) {
	reloadError := strings.Repeat("x", projectDevelopmentSyncErrorMaxBytes+100)
	body := []byte("{\"reloadError\":\"" + reloadError + "\"}")
	err := validateProjectComponentSyncResponse("server", 200, body)
	if err == nil {
		t.Fatal("validate sync response succeeded, want bounded reload failure")
	}
	if len(err.Error()) > projectDevelopmentSyncErrorMaxBytes+100 {
		t.Fatalf("reload error length = %d, want bounded response", len(err.Error()))
	}
}
