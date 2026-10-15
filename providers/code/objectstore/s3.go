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
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Environment variables the S3 backend reads. CODE_BUNDLE_STORE selects the
// backend for both transient stores; the rest configure S3.
const (
	// EnvStore is "file" (the default: a local directory, one replica only)
	// or "s3" (any S3-compatible service; required for replicaCount > 1).
	EnvStore = "CODE_BUNDLE_STORE"
	// EnvS3Endpoint is host[:port], optionally with an http:// or https://
	// scheme that also decides EnvS3UseSSL. AWS: s3.<region>.amazonaws.com.
	EnvS3Endpoint = "CODE_S3_ENDPOINT"
	EnvS3Bucket   = "CODE_S3_BUCKET"
	EnvS3Region   = "CODE_S3_REGION"
	// EnvS3Prefix is a key prefix inside the bucket, so one bucket can be
	// shared with other tenants of the object store.
	EnvS3Prefix = "CODE_S3_PREFIX"
	// EnvS3AccessKeyID and EnvS3SecretAccessKey are static credentials. Leave
	// both unset to use the AWS chain (AWS_* env, shared credentials file,
	// IAM / IRSA / pod identity).
	EnvS3AccessKeyID     = "CODE_S3_ACCESS_KEY_ID"
	EnvS3SecretAccessKey = "CODE_S3_SECRET_ACCESS_KEY"
	// EnvS3UseSSL defaults to true; "false" talks plain HTTP (local MinIO).
	EnvS3UseSSL = "CODE_S3_USE_SSL"
	// EnvS3CreateBucket, when "true", creates the bucket if it is missing.
	// Meant for the dev loop; production buckets are provisioned outside.
	EnvS3CreateBucket = "CODE_S3_CREATE_BUCKET"

	StoreFile = "file"
	StoreS3   = "s3"
)

// S3Config configures an S3 backend.
type S3Config struct {
	Endpoint        string
	Bucket          string
	Region          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
	CreateBucket    bool
}

// SelectedStore reports which backend CODE_BUNDLE_STORE names, defaulting
// to StoreFile. Any other value is an error so a typo cannot silently run a
// multi-replica deployment on local disk.
func SelectedStore() (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvStore))); v {
	case "", StoreFile:
		return StoreFile, nil
	case StoreS3:
		return StoreS3, nil
	default:
		return "", fmt.Errorf("%s=%q: want %q or %q", EnvStore, v, StoreFile, StoreS3)
	}
}

// S3ConfigFromEnv reads the CODE_S3_* variables.
func S3ConfigFromEnv() (S3Config, error) {
	cfg := S3Config{
		Endpoint:        strings.TrimSpace(os.Getenv(EnvS3Endpoint)),
		Bucket:          strings.TrimSpace(os.Getenv(EnvS3Bucket)),
		Region:          strings.TrimSpace(os.Getenv(EnvS3Region)),
		Prefix:          strings.Trim(strings.TrimSpace(os.Getenv(EnvS3Prefix)), "/"),
		AccessKeyID:     strings.TrimSpace(os.Getenv(EnvS3AccessKeyID)),
		SecretAccessKey: strings.TrimSpace(os.Getenv(EnvS3SecretAccessKey)),
		UseSSL:          true,
	}
	if raw := strings.TrimSpace(os.Getenv(EnvS3UseSSL)); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return S3Config{}, fmt.Errorf("%s=%q: %w", EnvS3UseSSL, raw, err)
		}
		cfg.UseSSL = v
	}
	if raw := strings.TrimSpace(os.Getenv(EnvS3CreateBucket)); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return S3Config{}, fmt.Errorf("%s=%q: %w", EnvS3CreateBucket, raw, err)
		}
		cfg.CreateBucket = v
	}
	if cfg.Endpoint == "" {
		return S3Config{}, fmt.Errorf("%s is required when %s=%s", EnvS3Endpoint, EnvStore, StoreS3)
	}
	if cfg.Bucket == "" {
		return S3Config{}, fmt.Errorf("%s is required when %s=%s", EnvS3Bucket, EnvStore, StoreS3)
	}
	if (cfg.AccessKeyID == "") != (cfg.SecretAccessKey == "") {
		return S3Config{}, fmt.Errorf("%s and %s must be set together", EnvS3AccessKeyID, EnvS3SecretAccessKey)
	}
	return cfg, nil
}

// S3 is a Backend over one bucket (and optional key prefix) of any
// S3-compatible service: AWS S3, MinIO, Ceph RGW, GCS in interoperability
// mode, and so on.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
}

var _ Backend = (*S3)(nil)

// NewS3 connects to the configured bucket and verifies it is reachable, so a
// misconfigured deployment fails at startup rather than on the first commit.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	endpoint, secure, err := splitEndpoint(cfg.Endpoint, cfg.UseSSL)
	if err != nil {
		return nil, err
	}
	var creds *credentials.Credentials
	if cfg.AccessKeyID != "" {
		creds = credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.FileAWSCredentials{},
			&credentials.IAM{},
		})
	}
	client, err := minio.New(endpoint, &minio.Options{Creds: creds, Secure: secure, Region: cfg.Region})
	if err != nil {
		return nil, fmt.Errorf("s3 client for %s: %w", cfg.Endpoint, err)
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3 bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		if !cfg.CreateBucket {
			return nil, fmt.Errorf("s3 bucket %q does not exist at %s (set %s=true to create it in dev)", cfg.Bucket, cfg.Endpoint, EnvS3CreateBucket)
		}
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			// Another replica may have created it between the two calls.
			if again, existsErr := client.BucketExists(ctx, cfg.Bucket); existsErr != nil || !again {
				return nil, fmt.Errorf("create s3 bucket %q: %w", cfg.Bucket, err)
			}
		}
	}
	return &S3{client: client, bucket: cfg.Bucket, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

// NewS3FromEnv builds an S3 backend from the CODE_S3_* variables.
func NewS3FromEnv(ctx context.Context) (*S3, error) {
	cfg, err := S3ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return NewS3(ctx, cfg)
}

// splitEndpoint accepts "host:port" or a URL and returns the host and
// whether to use TLS: an explicit scheme wins over the UseSSL flag.
func splitEndpoint(raw string, useSSL bool) (string, bool, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false, fmt.Errorf("parse s3 endpoint %q: %w", raw, err)
		}
		switch u.Scheme {
		case "http":
			useSSL = false
		case "https":
			useSSL = true
		default:
			return "", false, fmt.Errorf("s3 endpoint %q: unsupported scheme %q", raw, u.Scheme)
		}
		if u.Host == "" || u.Path != "" && u.Path != "/" {
			return "", false, fmt.Errorf("s3 endpoint %q must be a bare host[:port]", raw)
		}
		return u.Host, useSSL, nil
	}
	if raw == "" || strings.Contains(raw, "/") {
		return "", false, fmt.Errorf("s3 endpoint %q must be host[:port]", raw)
	}
	return raw, useSSL, nil
}

// String names the backend for logs.
func (s *S3) String() string {
	if s.prefix == "" {
		return "s3://" + s.bucket
	}
	return "s3://" + s.bucket + "/" + s.prefix
}

func (s *S3) key(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3) unkey(full string) string {
	if s.prefix == "" {
		return full
	}
	return strings.TrimPrefix(full, s.prefix+"/")
}

// Put writes data at key, replacing any existing object.
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.key(key), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Get reads the whole object at key.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, classify(err))
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(io.LimitReader(obj, MaxObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("s3 read %s: %w", key, classify(err))
	}
	if int64(len(data)) > MaxObjectBytes {
		return nil, fmt.Errorf("s3 object %s: %w (%d bytes)", key, ErrTooLarge, MaxObjectBytes)
	}
	return data, nil
}

// Stat describes the object at key.
func (s *S3) Stat(ctx context.Context, key string) (Object, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.key(key), minio.StatObjectOptions{})
	if err != nil {
		return Object{}, fmt.Errorf("s3 stat %s: %w", key, classify(err))
	}
	return Object{Key: key, Size: info.Size, LastModified: info.LastModified}, nil
}

// Delete removes the object at key. S3 reports success for a missing key.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, s.key(key), minio.RemoveObjectOptions{}); err != nil {
		if IsNotFound(classify(err)) {
			return nil
		}
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// List calls fn for every object under prefix.
func (s *S3) List(ctx context.Context, prefix string, fn func(Object) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for info := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key(prefix), Recursive: true}) {
		if info.Err != nil {
			return fmt.Errorf("s3 list %s: %w", prefix, info.Err)
		}
		if err := fn(Object{Key: s.unkey(info.Key), Size: info.Size, LastModified: info.LastModified}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// classify maps the service's not-found answers onto ErrNotFound.
func classify(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NotFound":
		return fmt.Errorf("%w: %s", ErrNotFound, resp.Code)
	case "NoSuchBucket":
		// A missing bucket is a deployment fault, not a missing object: a
		// consumer must not wait out its arrival window on it.
		return err
	}
	var target minio.ErrorResponse
	if errors.As(err, &target) && target.StatusCode == 404 && target.Code == "" {
		return fmt.Errorf("%w: %s", ErrNotFound, target.Message)
	}
	return err
}
