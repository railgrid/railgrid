/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	gogithub "github.com/google/go-github/v66/github"

	"github.com/faroshq/provider-code/backend"
)

// listingFlight coalesces one complete listing independently of the HTTP gate.
// Results are immutable serialized snapshots, retained only until waiters return.
type listingFlight struct {
	done chan struct{}
	body []byte
	err  error
}

// cachedPackageListing publishes only complete successful paginated results.
// Each HTTP request acquires the credential gate separately so other operations
// can proceed between pages. No individual page is reused during a refresh.
func cachedPackageListing[T any](ctx context.Context, b *Backend, client *gogithub.Client, cred backend.Credential, parts []string, fetch func(context.Context) ([]T, error)) ([]T, error) {
	cache := b.requestCache()
	identity := requestIdentity{credentialHash(cred.Token), client.BaseURL.Scheme + "://" + client.BaseURL.Host}
	state, err := cache.acquire(ctx, identity)
	if err != nil {
		return nil, err
	}
	state.expire(cache.now())

	scope, _ := ctx.Value(cacheScopeKey{}).([32]byte)
	encodedKey, _ := json.Marshal(append([]string{client.BaseURL.String()}, parts...))
	key := sha256.Sum256(append(scope[:], encodedKey...))
	if cached, ok := state.responses[key]; ok {
		cache.release(state)
		var result []T
		err := json.Unmarshal(cached.body, &result)
		return result, err
	}
	if flight, ok := state.listings[key]; ok {
		cache.release(state)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			if flight.err != nil {
				return nil, flight.err
			}
			var result []T
			err := json.Unmarshal(flight.body, &result)
			return result, err
		}
	}
	if len(state.listings) >= maxCachedResponses {
		cache.release(state)
		return nil, fmt.Errorf("github: package refresh capacity reached; retry later")
	}
	if state.listings == nil {
		state.listings = make(map[[32]byte]*listingFlight)
	}
	flight := &listingFlight{done: make(chan struct{})}
	state.listings[key] = flight
	cache.release(state)

	ctx = context.WithValue(ctx, cacheTTLKey{}, time.Duration(0))
	result, err := fetch(ctx)
	var body []byte
	if err == nil {
		body, err = json.Marshal(result)
	}
	// An active flight prevents eviction. Always finish publication and wake
	// waiters, including when the initiating context has been cancelled.
	<-state.gate
	if err == nil {
		state.store(key, cachedResponse{status: http.StatusOK, body: body, expires: cache.now().Add(packageCacheTTL)})
	}
	flight.body, flight.err = body, state.retryError(err, cache.now())
	delete(state.listings, key)
	close(flight.done)
	state.gate <- struct{}{}
	if flight.err != nil {
		return nil, flight.err
	}
	return result, nil
}
