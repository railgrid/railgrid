//go:build !unix

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

import "os"

// processAlive is a best-effort stand-in on platforms without POSIX signals.
// Add-ons are not supported there, so the tests that use it do not run.
func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
