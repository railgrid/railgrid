/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package deploykey reconciles DeployKey CRs. When spec.publicKey is empty the
// controller generates an ed25519 keypair, registers the public half on the
// repository via the git backend, and stores the private half in a Secret in
// the tenant workspace (status.secretRef, owned by the DeployKey CR) — the
// cross-provider seam another provider mounts to clone/push. A BYO public key
// (spec.publicKey set) is registered as-is with no Secret.
package deploykey

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/code/backend"
	"github.com/faroshq/faros-kedge/providers/code/controller/shared"
	"github.com/faroshq/faros-kedge/providers/code/tenant"
)

// secretDataKeyPrivate / secretDataKeyPublic are the Secret data keys the
// generated keypair is written under. ssh-privatekey matches the well-known key
// kubernetes.io/ssh-auth Secrets use.
const (
	secretDataKeyPrivate = "ssh-privatekey"
	secretDataKeyPublic  = "ssh-publickey"
)

// Reconciler manages DeployKey CRs.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-deploykey").
		For(&codev1alpha1.DeployKey{}).
		Complete(r)
}

// Reconcile ensures one DeployKey.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("deploykey", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var key codev1alpha1.DeployKey
	if err := c.Get(ctx, req.NamespacedName, &key); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	b, conn, repo, cred, ready := r.resolve(ctx, c, &key)

	// Deletion path.
	if !key.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&key, codev1alpha1.FinalizerDeployKey) {
			if ready && key.Status.KeyID != "" {
				if err := b.DeleteDeployKey(ctx, conn, cred, repo, key.Status.KeyID); err != nil {
					return r.fail(ctx, c, &key, "DeleteFailed", err.Error())
				}
			}
			// Explicitly delete the generated private-key Secret (the
			// ownerReference would GC it too, but be deterministic about it).
			if err := r.deleteSecret(ctx, c, &key); err != nil {
				return r.fail(ctx, c, &key, "SecretCleanupFailed", err.Error())
			}
			controllerutil.RemoveFinalizer(&key, codev1alpha1.FinalizerDeployKey)
			if err := c.Update(ctx, &key); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&key, codev1alpha1.FinalizerDeployKey) {
		if err := c.Update(ctx, &key); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !ready {
		return r.fail(ctx, c, &key, "RepositoryNotReady", "referenced repository, connection, or credential is not available yet")
	}

	// Resolve the public key to register: BYO, or generated (persisted in a Secret).
	publicKey, secretRef, err := r.ensurePublicKey(ctx, c, &key)
	if err != nil {
		return r.fail(ctx, c, &key, "KeyMaterialError", err.Error())
	}

	res, err := b.EnsureDeployKey(ctx, conn, cred, repo, &key, publicKey)
	if err != nil {
		return r.fail(ctx, c, &key, "EnsureFailed", err.Error())
	}

	key.Status.ObservedGeneration = key.Generation
	key.Status.KeyID = res.KeyID
	key.Status.SecretRef = secretRef
	shared.SetCondition(&key.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "", key.Generation)
	if err := c.Status().Update(ctx, &key); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("DeployKey ensured", "keyID", res.KeyID, "generated", secretRef != nil)
	return ctrl.Result{}, nil
}

// resolve loads the backend, Connection, Repository, and credential. ready is
// false (no error) when any piece is missing.
func (r *Reconciler) resolve(ctx context.Context, c client.Client, key *codev1alpha1.DeployKey) (backend.GitBackend, *codev1alpha1.Connection, *codev1alpha1.Repository, backend.Credential, bool) {
	repo, err := shared.ResolveRepository(ctx, c, key.Spec.RepositoryRef)
	if err != nil {
		return nil, nil, nil, backend.Credential{}, false
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return nil, nil, repo, backend.Credential{}, false
	}
	b, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return nil, conn, repo, backend.Credential{}, false
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return b, conn, repo, backend.Credential{}, false
	}
	return b, conn, repo, cred, true
}

// ensurePublicKey returns the public key to register plus the Secret reference
// (nil for BYO keys). For generated keys it is idempotent: an existing Secret's
// public half is reused rather than minting a new keypair each reconcile.
func (r *Reconciler) ensurePublicKey(ctx context.Context, c client.Client, key *codev1alpha1.DeployKey) (string, *codev1alpha1.LocalSecretReference, error) {
	if strings.TrimSpace(key.Spec.PublicKey) != "" {
		return strings.TrimSpace(key.Spec.PublicKey), nil, nil
	}

	ns := tenant.DefaultCredentialsNamespace()
	name := secretName(key)
	ref := &codev1alpha1.LocalSecretReference{Name: name, Namespace: ns, Key: secretDataKeyPrivate}

	// Reuse an existing Secret so we don't rotate the key on every reconcile.
	var existing corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing)
	if err == nil {
		if pub, ok := existing.Data[secretDataKeyPublic]; ok && len(pub) > 0 {
			return strings.TrimSpace(string(pub)), ref, nil
		}
		// Secret exists but lacks the public half — recreate below.
	} else if !apierrors.IsNotFound(err) {
		return "", nil, fmt.Errorf("get existing key secret: %w", err)
	}

	priv, pub, err := generateED25519(key.Name)
	if err != nil {
		return "", nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         codev1alpha1.SchemeGroupVersion.String(),
				Kind:               "DeployKey",
				Name:               key.Name,
				UID:                key.UID,
				Controller:         ptrBool(true),
				BlockOwnerDeletion: ptrBool(true),
			}},
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: map[string][]byte{
			secretDataKeyPrivate: priv,
			secretDataKeyPublic:  pub,
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if gerr := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing); gerr == nil {
				if p, ok := existing.Data[secretDataKeyPublic]; ok {
					return strings.TrimSpace(string(p)), ref, nil
				}
			}
		}
		return "", nil, fmt.Errorf("create key secret: %w", err)
	}
	return strings.TrimSpace(string(pub)), ref, nil
}

func (r *Reconciler) deleteSecret(ctx context.Context, c client.Client, key *codev1alpha1.DeployKey) error {
	if key.Status.SecretRef == nil {
		return nil
	}
	ns := key.Status.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Status.SecretRef.Name, Namespace: ns}}
	if err := c.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, key *codev1alpha1.DeployKey, reason, msg string) (ctrl.Result, error) {
	key.Status.ObservedGeneration = key.Generation
	shared.SetCondition(&key.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, key.Generation)
	if err := c.Status().Update(ctx, key); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, fmt.Errorf("%s: %s", reason, msg)
}

// secretName is the deterministic name of the generated private-key Secret.
func secretName(key *codev1alpha1.DeployKey) string {
	return "deploykey-" + key.Name
}

func ptrBool(b bool) *bool { return &b }
