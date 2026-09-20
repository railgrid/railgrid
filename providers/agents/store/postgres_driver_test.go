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

package store

import (
	"database/sql"
	"slices"
	"testing"
)

// OpenPostgres names the "postgres" driver, which only exists once lib/pq's
// init has run. That import is a blank one and nothing else in the package
// uses it, so an unused-import cleanup can drop it without a compile error;
// the provider then fails at startup with `unknown driver "postgres"`.
func TestPostgresDriverIsRegistered(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "postgres") {
		t.Fatalf("database/sql has no %q driver; store/postgres.go must blank-import github.com/lib/pq (registered: %v)", "postgres", sql.Drivers())
	}
}
