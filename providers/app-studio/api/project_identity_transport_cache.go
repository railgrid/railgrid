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

package api

import (
	"crypto/sha256"
	"net/http"
	"sync"
)

const projectProviderActionTransportCacheLimit = 8

type projectProviderActionTransportKey struct {
	base     *http.Transport
	trust    [sha256.Size]byte
	insecure bool
}

type projectProviderActionTransportCache struct {
	mu      sync.Mutex
	entries map[projectProviderActionTransportKey]*http.Transport
	order   []projectProviderActionTransportKey
}

var projectProviderActionTransports projectProviderActionTransportCache

// The shared transports contain only immutable TLS policy and connection
// pools. Project tokens and tenant scope stay on individual requests, outside
// this cache. CA files are read before lookup, so rotated trust selects a new
// pool immediately. Bounded eviction closes idle connections only.
func (cache *projectProviderActionTransportCache) load(
	base *http.Transport,
	bundle []byte,
	insecure bool,
	create func() (*http.Transport, error),
) (*http.Transport, error) {
	key := projectProviderActionTransportKey{base: base, trust: sha256.Sum256(bundle), insecure: insecure}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if transport := cache.entries[key]; transport != nil {
		return transport, nil
	}
	transport, err := create()
	if err != nil {
		return nil, err
	}
	if cache.entries == nil {
		cache.entries = make(map[projectProviderActionTransportKey]*http.Transport)
	}
	if len(cache.order) >= projectProviderActionTransportCacheLimit {
		oldest := cache.order[0]
		cache.entries[oldest].CloseIdleConnections()
		delete(cache.entries, oldest)
		cache.order = cache.order[1:]
	}
	cache.entries[key] = transport
	cache.order = append(cache.order, key)
	return transport, nil
}
