//go:build unix

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

package harness

import (
	"os"
	"syscall"
	"testing"
)

type environmentOwnerInfo struct {
	os.FileInfo
	uid uint32
}

func (f environmentOwnerInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }

func TestEnvironmentOwnerPolicy(t *testing.T) {
	for _, uid := range []uint32{0, uint32(os.Geteuid())} {
		if !TrustedFileOwner(environmentOwnerInfo{uid: uid}) {
			t.Fatal("trusted owner refused")
		}
	}
	foreign := uint32(os.Geteuid()) + 1
	if foreign == 0 {
		foreign++
	}
	if TrustedFileOwner(environmentOwnerInfo{uid: foreign}) {
		t.Fatal("foreign owner accepted")
	}
}
