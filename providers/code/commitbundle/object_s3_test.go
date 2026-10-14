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

package commitbundle

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

// TestObjectStoreAgainstS3 drives the bundle store through a real
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
		Prefix:          fmt.Sprintf("bundles-%d", time.Now().UnixNano()),
		AccessKeyID:     os.Getenv(objectstore.EnvS3AccessKeyID),
		SecretAccessKey: os.Getenv(objectstore.EnvS3SecretAccessKey),
		UseSSL:          true,
		CreateBucket:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two stores over one bucket stand in for two replicas.
	writer, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := writer.Sweep(context.Background(), time.Now().Add(48*time.Hour), DefaultSweepMaxAge); err != nil {
			t.Logf("cleanup sweep: %v", err)
		}
	})

	files := []File{{Path: "src/main.go", Content: "package main\n"}, {Path: "img.bin", Content: "AAEC", Encoding: EncodingBase64}}
	ref, err := writer.Put(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := reader.Get(ctx, "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatalf("second replica cannot read the bundle: %v", err)
	}
	if len(bundle.Files) != 2 || bundle.Files[1].Path != "src/main.go" {
		t.Fatalf("unexpected bundle %+v", bundle)
	}
	if _, err := reader.Get(ctx, "root:other", ref.Name, ref.Digest); !IsNotFound(err) {
		t.Fatalf("bundle visible across scopes: %v", err)
	}
	// Content-addressed reuse keeps one object; isolated puts make distinct ones.
	again, err := writer.Put(ctx, "root:acme", files)
	if err != nil || again.Name != ref.Name {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	iso1, err := writer.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	iso2, err := writer.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	if iso1.Name == iso2.Name || iso1.Digest != ref.Digest {
		t.Fatalf("isolated bundles: %+v %+v", iso1, iso2)
	}
	if err := reader.Delete(ctx, "root:acme", iso1.Name, iso1.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Get(ctx, "root:acme", iso1.Name, iso1.Digest); !IsNotFound(err) {
		t.Fatalf("deleted bundle still readable: %v", err)
	}
	if _, err := writer.Get(ctx, "root:acme", iso2.Name, iso2.Digest); err != nil {
		t.Fatalf("twin removed with its sibling: %v", err)
	}
	// Nothing is old enough to sweep yet; everything is after the clock jumps.
	if removed, err := writer.Sweep(ctx, time.Now(), DefaultSweepMaxAge); err != nil || removed != 0 {
		t.Fatalf("premature sweep: %d %v", removed, err)
	}
	removed, err := writer.Sweep(ctx, time.Now().Add(48*time.Hour), DefaultSweepMaxAge)
	if err != nil || removed != 2 {
		t.Fatalf("sweep: %d %v (want 2)", removed, err)
	}
}
