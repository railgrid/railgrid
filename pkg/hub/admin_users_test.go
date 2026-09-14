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

package hub

import (
	"reflect"
	"testing"

	"k8s.io/klog/v2"
)

func TestBuildAdminSet(t *testing.T) {
	got := buildAdminSet(klog.Background(), []string{" Faros:Static:47B9dce0e91570a1 ", "", "  ", "admin@example.com"})
	want := map[string]struct{}{
		"faros:static:47b9dce0e91570a1": {},
		"admin@example.com":             {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildAdminSet = %v, want %v", got, want)
	}
	// Static-token users have no email; an empty entry must never be able
	// to match them.
	if _, ok := got[""]; ok {
		t.Fatal("empty --admin-users entry is in the admin set")
	}
}
