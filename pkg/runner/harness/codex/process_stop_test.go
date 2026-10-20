/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
	"os/exec"
	"testing"
)

func TestStopAfterWaitClosedStdin(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"successful exit", "exit 0"},
		{"failed exit", "exit 7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", tc.script)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait() // Also closes stdin, before stop gets to it.
			p := &serverProcess{cmd: cmd, stdin: stdin, done: make(chan error, 1)}
			p.done <- waitErr
			got := p.stop()
			if waitErr == nil && got != nil {
				t.Fatalf("stop after successful exit: %v", got)
			}
			if waitErr != nil && !errors.Is(got, waitErr) {
				t.Fatalf("stop = %v, want child failure %v", got, waitErr)
			}
			if err := p.stop(); err != nil {
				t.Fatalf("repeated stop: %v", err)
			}
		})
	}
}

type failingStdin struct{ err error }

func (s failingStdin) Write(p []byte) (int, error) { return len(p), nil }
func (s failingStdin) Close() error                { return s.err }

func TestStopPreservesStdinCleanupFailure(t *testing.T) {
	want := errors.New("stdin cleanup failed")
	p := &serverProcess{stdin: failingStdin{err: want}, done: make(chan error, 1)}
	p.done <- nil
	if err := p.stop(); !errors.Is(err, want) {
		t.Fatalf("stop = %v, want %v", err, want)
	}
}
