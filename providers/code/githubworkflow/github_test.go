// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import "testing"

func TestParseAndSignature(t *testing.T) {
	for _, tt := range []struct {
		body, mode string
		addressed  bool
	}{
		{"/railgrid review", "review", true},
		{"/railgrid review-fix\r\nScope: foo", "review-fix", true},
		{"/railgrid review later", "", true},
		{"/railgrid unknown", "", true},
		{"> /railgrid review", "", false},
		{"Example:\n/railgrid review", "", false},
		{"```\n/railgrid review\n```", "", false},
	} {
		mode, addressed := Parse(tt.body)
		if mode != tt.mode || addressed != tt.addressed {
			t.Fatalf("Parse(%q) = %q, %v", tt.body, mode, addressed)
		}
	}
	key := []byte("a-long-32-byte-test-signing-secret")
	body := []byte("payload")
	sig := "sha256=" + Signature(key, body)
	if !Verify(key, body, sig) || Verify(key, []byte("changed"), sig) || Verify([]byte("short"), body, sig) || Verify(key, body, "sha256=zz") {
		t.Fatal("signature verification failed")
	}
}
