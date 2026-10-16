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

package kcp

import (
	"testing"

	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

func TestConfigureEmbeddedEtcdPorts(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		client, peer         int
		wantClient, wantPeer string
		invalid              bool
	}{
		{"defaults", 0, 0, "2379", "2380", false},
		{"isolated", 20001, 20002, "20001", "20002", false},
		{"client only", 20001, 0, "20001", "2380", false},
		{"negative", -1, 0, "", "", true},
		{"overflow", 0, 65536, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := serveroptions.NewOptions(t.TempDir())
			err := configureEmbeddedEtcd(opts, tc.client, tc.peer)
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.invalid {
				return
			}
			if !opts.EmbeddedEtcd.Enabled || opts.EmbeddedEtcd.ClientPort != tc.wantClient || opts.EmbeddedEtcd.PeerPort != tc.wantPeer {
				t.Fatalf("embedded etcd ports: enabled=%v client=%s peer=%s", opts.EmbeddedEtcd.Enabled, opts.EmbeddedEtcd.ClientPort, opts.EmbeddedEtcd.PeerPort)
			}
		})
	}
}
