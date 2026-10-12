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

package serviceaccounts

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

const (
	appIdentityKeyRingSecretName = "railgrid-app-identity-keyring"
	appIdentityKeyRingVersion    = "1"
	appIdentitySigningKeyField   = "signing-private-key"
	appIdentitySubjectKeyField   = "subject-pseudonym-key"
	appIdentityPreviousKeysField = "previous-public-keys"
	appIdentityKeyRotationGrace  = 15 * time.Minute
)

// AppIdentityPublicKey carries a verification key and the time through which
// it remains valid. ExpiresAt is zero for the current key.
type AppIdentityPublicKey struct {
	Key       ed25519.PublicKey
	ExpiresAt time.Time
}

type storedAppIdentityPublicKey struct {
	Key       []byte    `json:"key"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// AppIdentityKeyRing stores Railgrid Auth's signing and pseudonym keys in the
// hub-internal workspace shared by every replica. The pseudonym key is stable
// for the lifetime of the ring; explicit signing-key rotation retains the old
// public key for one assertion lifetime so app sessions already issued can
// still verify.
type AppIdentityKeyRing struct {
	client    kubernetes.Interface
	namespace string
	now       func() time.Time
	random    io.Reader
}

// NewKCPAppIdentityKeyRing builds a key ring against the hub-internal kcp
// workspace in config and the namespace within it.
func NewKCPAppIdentityKeyRing(config *rest.Config, namespace string) (*AppIdentityKeyRing, error) {
	if config == nil || namespace == "" {
		return nil, fmt.Errorf("app identity key ring needs a config and namespace")
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("building app identity key ring client: %w", err)
	}
	return newKCPAppIdentityKeyRingWithClient(client, namespace, time.Now, rand.Reader), nil
}

func newKCPAppIdentityKeyRingWithClient(client kubernetes.Interface, namespace string, now func() time.Time, random io.Reader) *AppIdentityKeyRing {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &AppIdentityKeyRing{client: client, namespace: namespace, now: now, random: random}
}

// CurrentAppIdentitySigningKey returns the current Ed25519 private key.
func (r *AppIdentityKeyRing) CurrentAppIdentitySigningKey(ctx context.Context) (ed25519.PrivateKey, error) {
	secret, err := r.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	privateKey := append(ed25519.PrivateKey(nil), secret.Data[appIdentitySigningKeyField]...)
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("app identity key ring %s/%s has an invalid signing key", r.namespace, appIdentityKeyRingSecretName)
	}
	return privateKey, nil
}

// AppIdentitySubjectKey returns the stable key used to derive app-scoped
// pseudonymous subjects. It is independent of the signing key and never
// changes when the signing key rotates.
func (r *AppIdentityKeyRing) AppIdentitySubjectKey(ctx context.Context) ([]byte, error) {
	secret, err := r.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	key := append([]byte(nil), secret.Data[appIdentitySubjectKeyField]...)
	if len(key) < delegatedProofKeyMinLength {
		return nil, fmt.Errorf("app identity key ring %s/%s has an invalid subject key", r.namespace, appIdentityKeyRingSecretName)
	}
	return key, nil
}

// AppIdentityPublicKeys returns the current verification key followed by all
// prior keys whose overlap window has not expired.
func (r *AppIdentityKeyRing) AppIdentityPublicKeys(ctx context.Context) ([]AppIdentityPublicKey, error) {
	secret, err := r.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	privateKey := ed25519.PrivateKey(secret.Data[appIdentitySigningKeyField])
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("app identity key ring %s/%s has an invalid signing key", r.namespace, appIdentityKeyRingSecretName)
	}
	keys := []AppIdentityPublicKey{{Key: append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)}}
	previous, err := decodeAppIdentityPreviousKeys(secret.Data[appIdentityPreviousKeysField])
	if err != nil {
		return nil, err
	}
	now := r.now()
	for _, entry := range previous {
		if len(entry.Key) != ed25519.PublicKeySize || !now.Before(entry.ExpiresAt) {
			continue
		}
		keys = append(keys, AppIdentityPublicKey{
			Key:       append(ed25519.PublicKey(nil), entry.Key...),
			ExpiresAt: entry.ExpiresAt,
		})
	}
	return keys, nil
}

// RotateAppIdentitySigningKey explicitly rotates assertion signing and keeps
// the previous public key available for 15 minutes. It is deliberately not
// called from hub startup or request handling.
func (r *AppIdentityKeyRing) RotateAppIdentitySigningKey(ctx context.Context) error {
	if r == nil || r.client == nil || r.namespace == "" {
		return fmt.Errorf("app identity key ring is unavailable")
	}
	secrets := r.client.CoreV1().Secrets(r.namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if _, err := r.getOrCreate(ctx); err != nil {
			return err
		}
		// Re-fetch so this update is based on the latest keyring and resource
		// version, even if another replica rotated concurrently.
		secret, err := secrets.Get(ctx, appIdentityKeyRingSecretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("reading app identity key ring for rotation: %w", err)
		}
		privateKey := ed25519.PrivateKey(secret.Data[appIdentitySigningKeyField])
		if len(privateKey) != ed25519.PrivateKeySize || len(secret.Data[appIdentitySubjectKeyField]) < delegatedProofKeyMinLength {
			return fmt.Errorf("app identity key ring %s/%s is malformed", r.namespace, appIdentityKeyRingSecretName)
		}
		previous, err := decodeAppIdentityPreviousKeys(secret.Data[appIdentityPreviousKeysField])
		if err != nil {
			return err
		}
		now := r.now().UTC()
		validPrevious := make([]storedAppIdentityPublicKey, 0, len(previous)+1)
		for _, entry := range previous {
			if len(entry.Key) == ed25519.PublicKeySize && now.Before(entry.ExpiresAt) {
				validPrevious = append(validPrevious, entry)
			}
		}
		validPrevious = append([]storedAppIdentityPublicKey{{
			Key:       append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...),
			ExpiresAt: now.Add(appIdentityKeyRotationGrace),
		}}, validPrevious...)

		seed := make([]byte, ed25519.SeedSize)
		if _, err := io.ReadFull(r.random, seed); err != nil {
			return fmt.Errorf("generating app identity signing key: %w", err)
		}
		newPrivateKey := ed25519.NewKeyFromSeed(seed)
		newPrevious, err := json.Marshal(validPrevious)
		if err != nil {
			return fmt.Errorf("encoding previous app identity keys: %w", err)
		}
		updated := secret.DeepCopy()
		updated.Data[appIdentitySigningKeyField] = append([]byte(nil), newPrivateKey...)
		updated.Data[appIdentityPreviousKeysField] = newPrevious
		if _, err := secrets.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return nil
	})
}

func (r *AppIdentityKeyRing) getOrCreate(ctx context.Context) (*corev1.Secret, error) {
	if r == nil || r.client == nil || r.namespace == "" {
		return nil, fmt.Errorf("app identity key ring is unavailable")
	}
	secrets := r.client.CoreV1().Secrets(r.namespace)
	secret, err := secrets.Get(ctx, appIdentityKeyRingSecretName, metav1.GetOptions{})
	if err == nil {
		if err := validateAppIdentityKeyRingSecret(secret); err != nil {
			return nil, err
		}
		return secret, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("reading app identity key ring: %w", err)
	}
	created, err := r.newSecret()
	if err != nil {
		return nil, err
	}
	secret, err = secrets.Create(ctx, created, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		secret, err = secrets.Get(ctx, appIdentityKeyRingSecretName, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("creating app identity key ring: %w", err)
	}
	if err := validateAppIdentityKeyRingSecret(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func (r *AppIdentityKeyRing) newSecret() (*corev1.Secret, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r.random, seed); err != nil {
		return nil, fmt.Errorf("generating app identity signing key: %w", err)
	}
	subjectKey := make([]byte, delegatedProofKeyLength)
	if _, err := io.ReadFull(r.random, subjectKey); err != nil {
		return nil, fmt.Errorf("generating app identity subject key: %w", err)
	}
	previous, err := json.Marshal([]storedAppIdentityPublicKey{})
	if err != nil {
		return nil, fmt.Errorf("encoding initial app identity keyring: %w", err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: appIdentityKeyRingSecretName, Namespace: r.namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"version":                    []byte(appIdentityKeyRingVersion),
			appIdentitySigningKeyField:   ed25519.NewKeyFromSeed(seed),
			appIdentitySubjectKeyField:   subjectKey,
			appIdentityPreviousKeysField: previous,
		},
	}, nil
}

func validateAppIdentityKeyRingSecret(secret *corev1.Secret) error {
	if secret == nil || string(secret.Data["version"]) != appIdentityKeyRingVersion ||
		len(secret.Data[appIdentitySigningKeyField]) != ed25519.PrivateKeySize ||
		len(secret.Data[appIdentitySubjectKeyField]) < delegatedProofKeyMinLength {
		return fmt.Errorf("app identity key ring is missing or malformed")
	}
	_, err := decodeAppIdentityPreviousKeys(secret.Data[appIdentityPreviousKeysField])
	if err != nil {
		return err
	}
	return nil
}

func decodeAppIdentityPreviousKeys(raw []byte) ([]storedAppIdentityPublicKey, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var keys []storedAppIdentityPublicKey
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("decoding previous app identity public keys: %w", err)
	}
	for _, key := range keys {
		if len(key.Key) != ed25519.PublicKeySize || key.ExpiresAt.IsZero() {
			return nil, fmt.Errorf("app identity key ring contains a malformed previous public key")
		}
	}
	return keys, nil
}
