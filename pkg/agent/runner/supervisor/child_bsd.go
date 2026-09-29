//go:build unix && !linux

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

package supervisor

import "syscall"

// bindToParent has no kernel support on macOS and the BSDs: a killed agent's
// child survives it. ReclaimStaleHolder, run before the next agent starts its
// first child, is what recovers the state lock and the port there.
func bindToParent(*syscall.SysProcAttr) {}
