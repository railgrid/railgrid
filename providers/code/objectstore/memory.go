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
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Backend for tests. It is safe for concurrent use
// and keeps the same semantics a bucket has: keys are opaque, Put replaces,
// List is by prefix.
type Memory struct {
	// Now supplies object timestamps; nil uses time.Now.
	Now func() time.Time

	mu      sync.Mutex
	objects map[string]memoryObject
}

type memoryObject struct {
	data     []byte
	modified time.Time
}

var _ Backend = (*Memory)(nil)

// NewMemory returns an empty in-memory backend.
func NewMemory() *Memory {
	return &Memory{objects: map[string]memoryObject{}}
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Put stores data at key.
func (m *Memory) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]byte, len(data))
	copy(copied, data)
	m.objects[key] = memoryObject{data: copied, modified: m.now()}
	return nil
}

// Get returns a copy of the object at key.
func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	copied := make([]byte, len(obj.data))
	copy(copied, obj.data)
	return copied, nil
}

// Stat describes the object at key.
func (m *Memory) Stat(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return Object{}, ErrNotFound
	}
	return Object{Key: key, Size: int64(len(obj.data)), LastModified: obj.modified}, nil
}

// Delete removes the object at key; a missing key is not an error.
func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// List calls fn for every object whose key starts with prefix, in key order.
func (m *Memory) List(ctx context.Context, prefix string, fn func(Object) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	objects := make([]Object, 0, len(keys))
	for _, key := range keys {
		obj := m.objects[key]
		objects = append(objects, Object{Key: key, Size: int64(len(obj.data)), LastModified: obj.modified})
	}
	m.mu.Unlock()
	for _, obj := range objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(obj); err != nil {
			return err
		}
	}
	return nil
}

// Touch rewrites the timestamp of key. Tests use it to age objects.
func (m *Memory) Touch(key string, modified time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return false
	}
	obj.modified = modified
	m.objects[key] = obj
	return true
}

// Len reports how many objects the backend holds.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objects)
}
