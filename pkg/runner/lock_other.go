//go:build !linux && !darwin

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

package runner

import (
	"fmt"
	"os"
)

// Other operating systems are intentionally unsupported by the runner binary
// today. Keeping this implementation buildable makes the package's failure
// explicit instead of silently permitting two processes to share state.
type processLock struct{ file *os.File }

func acquireProcessLock(_ string) (*processLock, error) {
	return nil, fmt.Errorf("runner process locking is unsupported on this operating system")
}

func (l *processLock) Close() error { return nil }
