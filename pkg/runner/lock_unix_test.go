//go:build linux || darwin

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

package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lock file names its holder so the agent that owns the state directory
// can reclaim it from a runner a previous agent left behind, and a second
// runner refused the lock can say who has it.
func TestProcessLockRecordsAndReportsItsHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.lock")
	lock, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close() //nolint:errcheck
	if got := lockHolder(path); got != os.Getpid() {
		t.Fatalf("lock records pid %d, want %d", got, os.Getpid())
	}
	_, err = acquireProcessLock(path)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("already locked by pid %d", os.Getpid())) {
		t.Fatalf("second acquisition = %v, want a refusal naming pid %d", err, os.Getpid())
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = again.Close()
}
