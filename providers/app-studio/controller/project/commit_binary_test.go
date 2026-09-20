/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/railgrid/provider-app-studio/workspace"
)

func commitTestPNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13}, bytes.Repeat([]byte{0xff, 0x00}, 64)...)
}

func (env *commitTestEnv) writeBytes(path string, data []byte) {
	env.t.Helper()
	if _, err := env.files.PutFile(env.ctx, env.scope, workspace.PutOptions{Path: path, Data: data}); err != nil {
		env.t.Fatal(err)
	}
	if _, err := env.files.AddUncommittedPaths(env.ctx, env.scope, []string{path}); err != nil {
		env.t.Fatal(err)
	}
}

// Binaries travel base64 unconditionally. There is nothing to probe for any
// more: the commit action's declared input schema carries the encoding, so a
// provider that serves the action accepts it, and a provider that does not
// serve it fails the route rather than silently writing base64 into a file.
func TestCommitWorkspaceSendsBinariesAsBase64(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	image := commitTestPNG()
	env.writeBytes("public/logo.png", image)

	dirty, err := env.commit()
	if err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v", dirty, err)
	}
	if env.commits() != 1 {
		t.Fatalf("commit calls = %d", env.commits())
	}
	files, _ := env.calls[0].input["files"].([]any)
	var found bool
	for _, raw := range files {
		file := raw.(map[string]any)
		if file["path"] != "public/logo.png" {
			if _, hasEncoding := file["encoding"]; hasEncoding {
				t.Fatalf("text file carries an encoding: %v", file)
			}
			continue
		}
		found = true
		decoded, err := base64.StdEncoding.DecodeString(file["content"].(string))
		if file["encoding"] != "base64" || err != nil || !bytes.Equal(decoded, image) {
			t.Fatalf("binary entry = %v (decode err %v)", file, err)
		}
	}
	if !found {
		t.Fatalf("binary missing from commit: %v", files)
	}
}
