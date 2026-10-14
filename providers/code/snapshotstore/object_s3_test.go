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

package snapshotstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

// TestObjectStoreAgainstS3 drives the snapshot store through a real
// S3-compatible service named by CODE_TEST_S3_ENDPOINT (see
// objectstore.TestS3BackendAgainstService for the variables). Skipped
// otherwise.
func TestObjectStoreAgainstS3(t *testing.T) {
	endpoint := os.Getenv("CODE_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("CODE_TEST_S3_ENDPOINT not set")
	}
	bucket := os.Getenv("CODE_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "railgrid-code-test"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	backend, err := objectstore.NewS3(ctx, objectstore.S3Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Prefix:          fmt.Sprintf("snapshots-%d", time.Now().UnixNano()),
		AccessKeyID:     os.Getenv(objectstore.EnvS3AccessKeyID),
		SecretAccessKey: os.Getenv(objectstore.EnvS3SecretAccessKey),
		UseSSL:          true,
		CreateBucket:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stager, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := stager.Sweep(context.Background(), time.Now().Add(2*TTL)); err != nil {
			t.Logf("cleanup sweep: %v", err)
		}
	})

	data := []byte(`{"commit":"abc","bundle":"private"}`)
	ref, err := stager.Stage(ctx, "cluster-1", "repo\x00conn\x00caller", data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := stager.Load(ctx, "cluster-1", "repo\x00conn\x00caller", ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("load: %q %v", got, err)
	}
	found, err := leader.Find(ctx, "cluster-1", ref)
	if err != nil || !bytes.Equal(found, data) {
		t.Fatalf("leader find: %q %v", found, err)
	}
	if _, err := leader.Find(ctx, "cluster-2", ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot crossed the tenant boundary: %v", err)
	}
	if err := backend.Put(ctx, snapshotKey("cluster-1", "repo\x00conn\x00caller", ref), []byte(`{"commit":"tampered"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := leader.Find(ctx, "cluster-1", ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
	removed, err := stager.Sweep(ctx, time.Now().Add(2*TTL))
	if err != nil || removed != 1 {
		t.Fatalf("sweep: %d %v (want 1)", removed, err)
	}
}
