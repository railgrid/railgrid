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

package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSplitEndpoint(t *testing.T) {
	cases := []struct {
		in     string
		useSSL bool
		host   string
		secure bool
		err    bool
	}{
		{in: "localhost:9000", useSSL: false, host: "localhost:9000", secure: false},
		{in: "s3.eu-west-1.amazonaws.com", useSSL: true, host: "s3.eu-west-1.amazonaws.com", secure: true},
		{in: "http://localhost:9000", useSSL: true, host: "localhost:9000", secure: false},
		{in: "https://minio.minio.svc", useSSL: false, host: "minio.minio.svc", secure: true},
		{in: "https://minio.minio.svc/", useSSL: false, host: "minio.minio.svc", secure: true},
		{in: "https://minio.minio.svc/bucket", err: true},
		{in: "ftp://x", err: true},
		{in: "", err: true},
		{in: "host/with/path", err: true},
	}
	for _, c := range cases {
		host, secure, err := splitEndpoint(c.in, c.useSSL)
		if c.err {
			if err == nil {
				t.Errorf("%q: expected error, got %s %v", c.in, host, secure)
			}
			continue
		}
		if err != nil || host != c.host || secure != c.secure {
			t.Errorf("%q: got %s %v %v, want %s %v", c.in, host, secure, err, c.host, c.secure)
		}
	}
}

func TestSelectedStoreAndConfigFromEnv(t *testing.T) {
	t.Setenv(EnvStore, "")
	if kind, err := SelectedStore(); err != nil || kind != StoreFile {
		t.Fatalf("default store: %s %v", kind, err)
	}
	t.Setenv(EnvStore, "S3")
	if kind, err := SelectedStore(); err != nil || kind != StoreS3 {
		t.Fatalf("s3 store: %s %v", kind, err)
	}
	t.Setenv(EnvStore, "postgres")
	if _, err := SelectedStore(); err == nil {
		t.Fatal("unknown store accepted")
	}

	t.Setenv(EnvS3Endpoint, "")
	t.Setenv(EnvS3Bucket, "b")
	if _, err := S3ConfigFromEnv(); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	t.Setenv(EnvS3Endpoint, "localhost:9000")
	t.Setenv(EnvS3Bucket, "")
	if _, err := S3ConfigFromEnv(); err == nil {
		t.Fatal("missing bucket accepted")
	}
	t.Setenv(EnvS3Bucket, "b")
	t.Setenv(EnvS3AccessKeyID, "ak")
	t.Setenv(EnvS3SecretAccessKey, "")
	if _, err := S3ConfigFromEnv(); err == nil {
		t.Fatal("half a credential accepted")
	}
	t.Setenv(EnvS3SecretAccessKey, "sk")
	t.Setenv(EnvS3UseSSL, "false")
	t.Setenv(EnvS3CreateBucket, "true")
	t.Setenv(EnvS3Prefix, "/code/")
	cfg, err := S3ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UseSSL || !cfg.CreateBucket || cfg.Prefix != "code" || cfg.AccessKeyID != "ak" {
		t.Fatalf("unexpected config %+v", cfg)
	}
	t.Setenv(EnvS3UseSSL, "maybe")
	if _, err := S3ConfigFromEnv(); err == nil {
		t.Fatal("invalid bool accepted")
	}
}

// TestS3BackendAgainstService runs the real backend against an S3-compatible
// service named by CODE_TEST_S3_ENDPOINT (plus CODE_TEST_S3_BUCKET and the
// CODE_S3_ACCESS_KEY_ID / CODE_S3_SECRET_ACCESS_KEY pair), e.g. the dev
// MinIO from `make code-minio-up`:
//
//	CODE_TEST_S3_ENDPOINT=http://localhost:59000 CODE_TEST_S3_BUCKET=railgrid-code-test \
//	CODE_S3_ACCESS_KEY_ID=railgrid CODE_S3_SECRET_ACCESS_KEY=railgrid-dev go test ./objectstore/ -run S3Backend
func TestS3BackendAgainstService(t *testing.T) {
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
	prefix := fmt.Sprintf("test-%d", time.Now().UnixNano())
	backend, err := NewS3(ctx, S3Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Prefix:          prefix,
		AccessKeyID:     os.Getenv(EnvS3AccessKeyID),
		SecretAccessKey: os.Getenv(EnvS3SecretAccessKey),
		UseSSL:          true,
		CreateBucket:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = backend.List(context.Background(), "", func(obj Object) error {
			return backend.Delete(context.Background(), obj.Key)
		})
	})

	if _, err := backend.Get(ctx, "missing"); !IsNotFound(err) {
		t.Fatalf("missing object: %v", err)
	}
	if _, err := backend.Stat(ctx, "missing"); !IsNotFound(err) {
		t.Fatalf("missing stat: %v", err)
	}
	if err := backend.Delete(ctx, "missing"); err != nil {
		t.Fatalf("deleting a missing object must succeed: %v", err)
	}
	data := []byte(`{"hello":"world"}`)
	if err := backend.Put(ctx, "a/b/one.json", data); err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, "a/c/two.json", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, "z/three.json", []byte("3")); err != nil {
		t.Fatal(err)
	}
	got, err := backend.Get(ctx, "a/b/one.json")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %q %v", got, err)
	}
	info, err := backend.Stat(ctx, "a/b/one.json")
	if err != nil || info.Size != int64(len(data)) || info.Key != "a/b/one.json" || time.Since(info.LastModified) > time.Hour {
		t.Fatalf("stat: %+v %v", info, err)
	}
	var keys []string
	if err := backend.List(ctx, "a/", func(obj Object) error {
		keys = append(keys, obj.Key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a/b/one.json" || keys[1] != "a/c/two.json" {
		t.Fatalf("list under prefix: %v", keys)
	}
	if err := backend.Delete(ctx, "a/b/one.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Get(ctx, "a/b/one.json"); !IsNotFound(err) {
		t.Fatalf("deleted object still readable: %v", err)
	}
}
