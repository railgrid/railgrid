// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"strings"
	"testing"
)

// TestParseServeOptionsAllowUnverifiedEnv pins the env form of the host key
// escape hatch. A malformed value must not be read as "false": that silently
// puts the provider on a different security posture than the operator asked
// for, in whichever direction the typo went.
func TestParseServeOptionsAllowUnverifiedEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		unset   bool
		args    []string
		want    bool
		wantErr bool
	}{
		{name: "unset", unset: true},
		{name: "empty is treated as unset", env: ""},
		{name: "true", env: "true", want: true},
		{name: "1", env: "1", want: true},
		{name: "TRUE", env: "TRUE", want: true},
		{name: "false", env: "false"},
		{name: "0", env: "0"},
		{name: "typo is rejected, not read as false", env: "treu", wantErr: true},
		{name: "yes is rejected", env: "yes", wantErr: true},
		{name: "the flag still works with the env unset", unset: true, args: []string{"--allow-unverified-ssh-host-key"}, want: true},
		{name: "env=false does not turn the flag off", env: "false", args: []string{"--allow-unverified-ssh-host-key"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unset {
				// t.Setenv has no unset form; set-then-unset keeps its
				// restore-on-cleanup behaviour.
				t.Setenv(allowUnverifiedEnvVar, "placeholder")
				if err := os.Unsetenv(allowUnverifiedEnvVar); err != nil {
					t.Fatalf("unsetting %s: %v", allowUnverifiedEnvVar, err)
				}
			} else {
				t.Setenv(allowUnverifiedEnvVar, tc.env)
			}

			opts, err := parseServeOptions(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s=%q, got none (allowUnverifiedSSHHostKey=%v)", allowUnverifiedEnvVar, tc.env, opts.allowUnverifiedSSHHostKey)
				}
				if !strings.Contains(err.Error(), allowUnverifiedEnvVar) || !strings.Contains(err.Error(), tc.env) {
					t.Fatalf("error %q should name both the variable and the value %q", err, tc.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServeOptions: %v", err)
			}
			if opts.allowUnverifiedSSHHostKey != tc.want {
				t.Fatalf("allowUnverifiedSSHHostKey = %v, want %v", opts.allowUnverifiedSSHHostKey, tc.want)
			}
		})
	}
}
