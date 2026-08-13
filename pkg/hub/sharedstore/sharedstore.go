/*
Copyright 2026 The Faros Authors.

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

// Package sharedstore is the hub's cross-replica key/value store for short-lived
// login state: browser sessions and published-app authorization codes.
//
// Both are minted by whichever replica served the request and redeemed by
// whichever replica the load balancer picks next, so process-local maps break as
// soon as the hub runs more than one pod. Entries live as Secrets in
// root:faros:system:controllers — a hub-internal workspace no tenant, provider,
// or user identity can reach — which keeps the hub's only hard dependency kcp.
//
// Values are opaque to this package. Keys are hashed into the object name, so
// neither a session handle nor an authorization code is recoverable from
// resource names in logs, audit records, or metrics.
package sharedstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// ErrNotFound is returned when a key is absent, expired, or already redeemed.
var ErrNotFound = errors.New("sharedstore: entry not found")

const (
	// LabelKind marks every Secret this package owns and names the logical
	// collection, so the sweeper can list one kind without touching the other.
	LabelKind = "faros.sh/shared-store"

	dataKeyValue     = "value"
	dataKeyExpiresAt = "expiresAt"
)

// Store is one logical collection of entries (all sharing a `kind`) inside a
// single kcp workspace namespace. It is safe for concurrent use — it holds no
// state beyond its client.
type Store struct {
	client    kubernetes.Interface
	namespace string
	kind      string
	now       func() time.Time
}

// New builds a Store. config must already target the workspace holding the
// entries (its Host carries the /clusters/<path> segment). kind must be a valid
// label value and DNS-name prefix — it namespaces one collection from another
// inside the same Kubernetes namespace.
func New(config *rest.Config, namespace, kind string) (*Store, error) {
	if config == nil {
		return nil, fmt.Errorf("sharedstore: config is required")
	}
	if namespace == "" || kind == "" {
		return nil, fmt.Errorf("sharedstore: namespace and kind are required")
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("sharedstore: building client: %w", err)
	}
	return &Store{client: client, namespace: namespace, kind: kind, now: time.Now}, nil
}

// Put writes value under key, replacing any existing entry, and stamps the
// expiry the reader enforces.
func (s *Store) Put(ctx context.Context, key string, value []byte, expiresAt time.Time) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.objectName(key),
			Namespace: s.namespace,
			Labels:    map[string]string{LabelKind: s.kind},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			dataKeyValue:     value,
			dataKeyExpiresAt: []byte(expiresAt.UTC().Format(time.RFC3339Nano)),
		},
	}
	_, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = s.client.CoreV1().Secrets(s.namespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("sharedstore: writing %s entry: %w", s.kind, err)
	}
	return nil
}

// Get returns the value stored under key. An expired entry reports ErrNotFound
// and is deleted opportunistically, so a stale record cannot outlive its expiry
// even if the sweeper is not running.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	name := s.objectName(key)
	secret, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sharedstore: reading %s entry: %w", s.kind, err)
	}
	if s.expired(secret) {
		_ = s.deleteExact(ctx, secret)
		return nil, ErrNotFound
	}
	return secret.Data[dataKeyValue], nil
}

// Take returns the value stored under key and removes it in the same breath.
// The delete is guarded by the resource version observed on read, so exactly one
// caller across every replica can redeem a given key — the guarantee a
// single-use authorization code depends on.
func (s *Store) Take(ctx context.Context, key string) ([]byte, error) {
	name := s.objectName(key)
	secret, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sharedstore: reading %s entry: %w", s.kind, err)
	}
	if err := s.deleteExact(ctx, secret); err != nil {
		// NotFound or Conflict means another replica redeemed it first; either
		// way this caller must not receive the value.
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sharedstore: redeeming %s entry: %w", s.kind, err)
	}
	if s.expired(secret) {
		return nil, ErrNotFound
	}
	return secret.Data[dataKeyValue], nil
}

// Delete removes an entry. It is idempotent, which makes logout safe to retry.
func (s *Store) Delete(ctx context.Context, key string) error {
	err := s.client.CoreV1().Secrets(s.namespace).Delete(ctx, s.objectName(key), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("sharedstore: deleting %s entry: %w", s.kind, err)
	}
	return nil
}

// GC deletes every expired entry of this kind and returns how many it removed.
// Readers already refuse expired entries, so this only reclaims space — run it
// on the elected replica.
func (s *Store) GC(ctx context.Context) (int, error) {
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelKind + "=" + s.kind,
	})
	if err != nil {
		return 0, fmt.Errorf("sharedstore: listing %s entries: %w", s.kind, err)
	}
	deleted := 0
	for i := range list.Items {
		secret := &list.Items[i]
		if !s.expired(secret) {
			continue
		}
		if err := s.deleteExact(ctx, secret); err != nil &&
			!apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return deleted, fmt.Errorf("sharedstore: deleting expired %s entry: %w", s.kind, err)
		}
		deleted++
	}
	return deleted, nil
}

// RunGC sweeps expired entries on interval until ctx is done. Intended to run
// only on the leader; a second sweeper would merely duplicate deletes.
func RunGC(ctx context.Context, interval time.Duration, stores ...*Store) {
	logger := klog.FromContext(ctx).WithName("sharedstore-gc")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, store := range stores {
				n, err := store.GC(ctx)
				if err != nil {
					logger.Error(err, "Sweeping expired entries failed", "kind", store.kind)
					continue
				}
				if n > 0 {
					logger.V(2).Info("Swept expired entries", "kind", store.kind, "count", n)
				}
			}
		}
	}
}

// deleteExact deletes exactly the object version that was read. The
// preconditions are what make Take single-use across replicas.
func (s *Store) deleteExact(ctx context.Context, secret *corev1.Secret) error {
	return s.client.CoreV1().Secrets(s.namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID:             &secret.UID,
			ResourceVersion: &secret.ResourceVersion,
		},
	})
}

func (s *Store) expired(secret *corev1.Secret) bool {
	raw, ok := secret.Data[dataKeyExpiresAt]
	if !ok {
		// An entry without an expiry stamp is malformed; treat it as expired so
		// it cannot linger indefinitely.
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return true
	}
	return !s.now().Before(expiresAt)
}

// objectName maps an arbitrary key onto a stable, collision-resistant, DNS-safe
// Secret name. Hashing is what keeps the key itself out of resource names.
func (s *Store) objectName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return s.kind + "-" + hex.EncodeToString(sum[:])
}
