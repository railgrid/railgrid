/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeAllowsMissingAccountWhenOpenAIAuthIsNotRequired(t *testing.T) {
	binary := fakeCodexBinaryMissingAccount(t)
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !info.Ready || len(info.Reasons) != 0 {
		t.Fatalf("probe rejected optional account for an unauthenticated provider: %+v", info)
	}
}

func fakeCodexBinaryMissingAccount(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	content := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo codex-cli 0.147.0; exit 0; fi\nFAROS_FAKE_MISSING_ACCOUNT_CHILD=1 exec %q -test.run=TestFakeMissingAccountAppServerProcess\n", os.Args[0])
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestFakeMissingAccountAppServerProcess(t *testing.T) {
	if os.Getenv("FAROS_FAKE_MISSING_ACCOUNT_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		method, _ := request["method"].(string)
		switch method {
		case "initialize":
			writeResponse(map[string]any{"id": request["id"], "result": map[string]any{"userAgent": "fake", "codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "unix", "platformOs": "linux"}})
		case "account/read":
			writeResponse(map[string]any{"id": request["id"], "result": map[string]any{"requiresOpenaiAuth": false}})
		}
	}
	os.Exit(0)
}
