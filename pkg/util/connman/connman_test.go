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

package connman

import (
	"context"
	"testing"
)

func TestConnectionManager_DialNoConnection(t *testing.T) {
	cm := New()

	_, err := cm.Dial(context.Background(), "nonexistent")
	if err != ErrNoConnection {
		t.Errorf("expected ErrNoConnection, got %v", err)
	}
}

func TestConnectionManager_New(t *testing.T) {
	cm := New()
	if cm == nil {
		t.Fatal("expected non-nil ConnectionManager")
	}
	if cm.deviceDialers == nil {
		t.Fatal("expected initialized deviceDialers map")
	}
}
