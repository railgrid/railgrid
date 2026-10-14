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

// Package objectstore is the one object-storage seam the code provider's
// transient stores share. Commit bundles (commitbundle) and staged git
// snapshots (snapshotstore) are the two things the provider keeps outside
// kcp; with one replica they live on a local volume, and with more than one
// they must live somewhere every replica reaches, because the replica that
// accepts an upload and the leader that consumes it are different pods.
//
// Backend is deliberately small: whole-object put/get, stat, delete and a
// prefix listing. That is what both stores need and no more, so a backend is
// a few dozen lines (S3 here, Memory for tests) and the stores stay testable
// without a network.
package objectstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports that no object exists at the requested key.
var ErrNotFound = errors.New("object not found")

// ErrTooLarge reports an object above MaxObjectBytes.
var ErrTooLarge = errors.New("object exceeds the size limit")

// MaxObjectBytes bounds what one Get buffers. Every store writes well below
// it (a commit bundle is at most 48 MiB of decoded content, a snapshot 36
// MiB serialized), so only a foreign or corrupt object trips it.
const MaxObjectBytes = 64 << 20

// IsNotFound reports whether err means the object does not exist.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Object describes one stored object.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Backend is whole-object storage addressed by key. Keys are slash-separated
// paths relative to the backend's root (a bucket prefix for S3). Put replaces
// an existing object atomically; Delete of a missing key succeeds.
type Backend interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Stat(ctx context.Context, key string) (Object, error)
	Delete(ctx context.Context, key string) error
	// List calls fn for every object under prefix until fn returns an error
	// or the listing ends. The order is unspecified.
	List(ctx context.Context, prefix string, fn func(Object) error) error
}
