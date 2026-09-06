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

package serviceaccounts

// The keyed proof that makes a delegated user identity unforgeable.
//
// A delegated ServiceAccount lives in namespace "default" of the caller's own
// team workspace, and the bootstrap binds every workspace member — not just
// admins — to cluster-admin there (pkg/hub/kcp/bootstrap.go
// ensureWorkspaceAdmin, reached for every member via
// EnsureChildWorkspaceAdmin). A member can therefore create a ServiceAccount,
// label it delegated, annotate it with any username they like, and mint a
// token for it with TokenRequest. Every consistency check the hub can make
// over that object compares inputs the same attacker wrote, so none of them
// can separate a hub-minted account from a hand-made one.
//
// The proof is the one field they cannot produce: an HMAC-SHA256 over the
// identity tuple, keyed by a secret that lives in a hub-internal kcp workspace
// no tenant identity can reach. The hub writes it at mint time; verification
// recomputes it and compares in constant time.
//
// The ServiceAccount UID is part of the MAC input on purpose. Without it an
// attacker could pre-create the account at its (publicly derivable) name, mint
// a long-lived token against it, and wait for the hub to stamp a valid proof
// onto that same object the first time the victim legitimately uses the
// provider — at which point the attacker's token would start resolving as the
// victim. Binding the UID means the hub must delete and recreate any account
// it finds unproven, and the recreated object has a UID the attacker's token
// was never issued against.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// AnnotationDelegatedProof carries the hub's keyed proof over the
	// delegated identity tuple. It is the ONLY field on a delegated
	// ServiceAccount that a tenant member cannot write for themselves; every
	// other label, annotation, and even the object name is derived from public
	// inputs inside a workspace where members hold cluster-admin.
	AnnotationDelegatedProof = "faros.sh/delegated-user-proof"

	// delegatedProofDomain versions and domain-separates the MAC input, so a
	// proof can never be replayed into a different keyed construction should
	// one ever share this key.
	delegatedProofDomain = "faros.sh/delegated-user-proof/v1"

	// delegatedProofKeySecretName is the Secret holding the HMAC key, in the
	// hub-internal workspace/namespace the hub already uses for its
	// cross-replica state (root:faros:system:controllers, namespace
	// faros-hub — see pkg/hub/kcp.HubSystemNamespace). Nothing grants a
	// tenant, provider, or user identity access to that workspace.
	delegatedProofKeySecretName = "faros-delegated-user-proof-key"
	delegatedProofKeySecretKey  = "key"

	// delegatedProofKeyLength is what a freshly generated key gets. Existing
	// keys are accepted at delegatedProofKeyMinLength or longer so the key can
	// be lengthened later without invalidating every account at once.
	delegatedProofKeyLength    = 32
	delegatedProofKeyMinLength = 32
)

// ProofKeySource yields the hub's delegated-identity signing key. It is
// consulted on the mint path and on every verification; implementations are
// expected to cache, since both paths are per-request.
type ProofKeySource interface {
	DelegatedProofKey(ctx context.Context) ([]byte, error)
}

// StaticProofKeySource serves a key the caller already holds. Used by tests
// and by callers that source the key some other way.
type StaticProofKeySource []byte

// DelegatedProofKey implements ProofKeySource.
func (s StaticProofKeySource) DelegatedProofKey(context.Context) ([]byte, error) {
	if len(s) < delegatedProofKeyMinLength {
		return nil, fmt.Errorf("delegated proof key is too short")
	}
	return s, nil
}

// KCPProofKeySource loads the delegated-identity key from a Secret in a
// hub-internal kcp workspace, creating it on first use. Every hub replica
// reads the same Secret, so the proof survives restarts and rescheduling and
// is identical across replicas — a token minted by one replica verifies at
// another.
type KCPProofKeySource struct {
	client    kubernetes.Interface
	namespace string

	mu  sync.Mutex
	key []byte
}

// NewKCPProofKeySource builds a key source against config, which must already
// target the hub-internal workspace (its Host carries the /clusters/<path>
// segment), and the namespace within it.
//
// SECURITY: the workspace this config points at must be one no tenant,
// provider, or user identity can reach. root:faros:system:controllers is that
// workspace today; it also holds the hub's leader-election Lease and the
// shared session Secrets, so a leak there is already a total compromise.
func NewKCPProofKeySource(config *rest.Config, namespace string) (*KCPProofKeySource, error) {
	if config == nil || strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("delegated proof key source needs a config and namespace")
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("building delegated proof key client: %w", err)
	}
	return &KCPProofKeySource{client: client, namespace: namespace}, nil
}

// newKCPProofKeySourceWithClient is the test seam for NewKCPProofKeySource.
func newKCPProofKeySourceWithClient(client kubernetes.Interface, namespace string) *KCPProofKeySource {
	return &KCPProofKeySource{client: client, namespace: namespace}
}

// DelegatedProofKey implements ProofKeySource. The key is read once and kept
// in memory for the process's lifetime; a failure is not cached, so a
// transient apiserver error is retried on the next request rather than
// disabling delegation until the hub restarts.
func (s *KCPProofKeySource) DelegatedProofKey(ctx context.Context) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("delegated proof key source is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.key) >= delegatedProofKeyMinLength {
		return s.key, nil
	}

	secrets := s.client.CoreV1().Secrets(s.namespace)
	existing, err := secrets.Get(ctx, delegatedProofKeySecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		key, keyErr := proofKeyFromSecret(existing)
		if keyErr != nil {
			return nil, keyErr
		}
		s.key = key
		return key, nil
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("reading delegated proof key: %w", err)
	}

	key := make([]byte, delegatedProofKeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating delegated proof key: %w", err)
	}
	created, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: delegatedProofKeySecretName, Namespace: s.namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{delegatedProofKeySecretKey: key},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Another replica created it first; its key is the authoritative one.
		created, err = secrets.Get(ctx, delegatedProofKeySecretName, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("creating delegated proof key: %w", err)
	}
	stored, err := proofKeyFromSecret(created)
	if err != nil {
		return nil, err
	}
	s.key = stored
	return stored, nil
}

func proofKeyFromSecret(secret *corev1.Secret) ([]byte, error) {
	key := secret.Data[delegatedProofKeySecretKey]
	if len(key) < delegatedProofKeyMinLength {
		return nil, fmt.Errorf("delegated proof key %s/%s is missing or too short",
			secret.Namespace, secret.Name)
	}
	return key, nil
}

// delegatedProofKey resolves the key from source, treating a nil source as
// "delegation is not configured here" — which must reject rather than skip.
func delegatedProofKey(ctx context.Context, source ProofKeySource) ([]byte, error) {
	if source == nil {
		return nil, fmt.Errorf("delegated user identities are not accepted: no proof key source configured")
	}
	key, err := source.DelegatedProofKey(ctx)
	if err != nil {
		return nil, err
	}
	if len(key) < delegatedProofKeyMinLength {
		return nil, fmt.Errorf("delegated proof key is too short")
	}
	return key, nil
}

// SignDelegatedUserServiceAccount writes the hub's keyed proof onto sa, which
// must already carry its identity annotations and its server-assigned UID.
//
// Production code mints through Manager.IssueDelegatedUserToken, which calls
// this as part of creating the account. It is exported so that tests outside
// this package — the hub's tenant resolver in particular — can build an
// account that is genuinely hub-minted, and contrast it with one a tenant
// member forged. Do not call it to bless an object the hub did not create:
// the whole point of the proof is that only the hub writes it.
func SignDelegatedUserServiceAccount(key []byte, sa *corev1.ServiceAccount, tenantPath string, user Identity, providerName string) error {
	if sa == nil || sa.UID == "" {
		return fmt.Errorf("delegated ServiceAccount needs a UID before it can be signed")
	}
	if len(key) < delegatedProofKeyMinLength {
		return fmt.Errorf("delegated proof key is too short")
	}
	if sa.Annotations == nil {
		sa.Annotations = map[string]string{}
	}
	sa.Annotations[AnnotationDelegatedProof] = computeDelegatedProof(key, tenantPath, user, providerName, sa.Name, sa.UID)
	return nil
}

// computeDelegatedProof is the MAC over one delegated identity. Fields are
// NUL-joined; every one of them is validated to be NUL-free before it reaches
// here (validateDelegatedInputs), so the encoding is unambiguous.
func computeDelegatedProof(key []byte, tenantPath string, user Identity, providerName, saName string, saUID types.UID) string {
	mac := hmac.New(sha256.New, key)
	// Writing to an hmac.Hash never returns an error.
	_, _ = mac.Write([]byte(strings.Join([]string{
		delegatedProofDomain,
		tenantPath,
		user.User,
		providerName,
		saName,
		string(saUID),
	}, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyDelegatedProof recomputes the MAC from the ServiceAccount's own
// annotations and compares it in constant time against the proof it carries.
//
// The annotations are attacker-controlled, and that is exactly why they are
// the MAC input: any edit to the identity they describe changes the expected
// MAC, and the attacker cannot recompute it. The UID and name are read off the
// object for the same reason — a copied proof does not travel to another
// object, and a deleted-and-recreated object does not inherit one.
func verifyDelegatedProof(key []byte, sa *corev1.ServiceAccount) error {
	if sa == nil {
		return fmt.Errorf("delegated ServiceAccount is missing")
	}
	if sa.UID == "" {
		return fmt.Errorf("delegated ServiceAccount has no UID")
	}
	presented, err := hex.DecodeString(strings.TrimSpace(sa.Annotations[AnnotationDelegatedProof]))
	if err != nil || len(presented) != sha256.Size {
		return fmt.Errorf("delegated ServiceAccount carries no valid hub proof")
	}
	want, err := hex.DecodeString(computeDelegatedProof(
		key,
		sa.Annotations[AnnotationWorkloadIdentityTenantPath],
		Identity{User: sa.Annotations[AnnotationDelegatedUser]},
		sa.Annotations[AnnotationDelegatedProvider],
		sa.Name,
		sa.UID,
	))
	if err != nil {
		return fmt.Errorf("computing delegated proof: %w", err)
	}
	if !hmac.Equal(presented, want) {
		return fmt.Errorf("delegated ServiceAccount carries no valid hub proof")
	}
	return nil
}
