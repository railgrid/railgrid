/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package packages reconciles a Repository's published host packages into
// dedicated Package CRs (one per artifact), owned by the Repository. It is a
// crawler, not a desired-state reconciler: GitHub has no per-repo packages API
// and rate-limits hard, so instead of the portal hitting the host on every page
// view, this controller lists the host on a timer (RequeueAfter) and reconciles
// the Package CR set to match. The portal then reads Package CRs straight from
// kcp (via the GraphQL gateway), never touching the host.
//
// Keyed on Repository (For) and owning Packages (Owns): on repository delete the
// owned Packages are garbage-collected via their OwnerReference, so the deletion
// path here is a no-op.
package packages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/code/backend"
	"github.com/faroshq/faros-kedge/providers/code/controller/shared"
)

// defaultCrawlInterval is how often each Repository is re-crawled for packages.
// Short enough that the portal sees fresh artifacts soon after a push, long
// enough that a workspace full of repos stays well under the host's rate limit.
// Override with CODE_PACKAGE_CRAWL_INTERVAL (any time.ParseDuration string).
const defaultCrawlInterval = 2 * time.Minute

// Reconciler crawls each Repository's host packages into Package CRs.
type Reconciler struct {
	Manager       mcmanager.Manager
	Backends      *backend.Registry
	CrawlInterval time.Duration
}

// SetupWithManager wires the reconciler into the multicluster manager. Keyed on
// Repository, owning the Package CRs it creates.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	if r.CrawlInterval == 0 {
		r.CrawlInterval = crawlIntervalFromEnv()
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-packages").
		For(&codev1alpha1.Repository{}).
		Owns(&codev1alpha1.Package{}).
		Complete(r)
}

// crawlIntervalFromEnv reads CODE_PACKAGE_CRAWL_INTERVAL, falling back to the
// default on empty/invalid input.
func crawlIntervalFromEnv() time.Duration {
	if v := os.Getenv("CODE_PACKAGE_CRAWL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultCrawlInterval
}

// Reconcile crawls one Repository's packages.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repository", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var repo codev1alpha1.Repository
	if err := c.Get(ctx, req.NamespacedName, &repo); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion: owned Packages are GC'd via their OwnerReference, nothing to do.
	if !repo.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Resolve the backend + credential the same way the repository reconciler
	// does. Any gap (wrong connectionRef, unknown provider, missing credential)
	// is transient for a crawler: log and retry next interval, never fail hard.
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		logger.V(4).Info("packages: connection not resolvable yet, will retry", "reason", err.Error())
		return ctrl.Result{RequeueAfter: r.CrawlInterval}, nil
	}
	b, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		logger.V(4).Info("packages: no backend for provider, skipping", "provider", conn.Spec.Provider)
		return ctrl.Result{}, nil
	}
	lister, ok := b.(backend.PackageLister)
	if !ok {
		// Backend can't list packages: make sure no stale Packages linger, then
		// stop crawling this repo (nothing will ever appear).
		if err := r.deleteAll(ctx, c, &repo); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		logger.V(4).Info("packages: credential not available yet, will retry", "reason", err.Error())
		return ctrl.Result{RequeueAfter: r.CrawlInterval}, nil
	}

	infos, err := lister.ListPackages(ctx, conn, cred, &repo)
	if err != nil {
		// Host error (throttle, transient): keep what we have, retry next pass.
		logger.V(2).Info("packages: host list failed, will retry", "error", err.Error())
		return ctrl.Result{RequeueAfter: r.CrawlInterval}, nil
	}

	if err := r.sync(ctx, c, &repo, infos); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(4).Info("packages crawled", "count", len(infos))
	return ctrl.Result{RequeueAfter: jitter(r.CrawlInterval, &repo)}, nil
}

// sync reconciles the set of Package CRs owned by repo to exactly match infos:
// create missing, update changed status, delete stale.
func (r *Reconciler) sync(ctx context.Context, c client.Client, repo *codev1alpha1.Repository, infos []backend.PackageInfo) error {
	existing, err := r.listOwned(ctx, c, repo)
	if err != nil {
		return err
	}

	desired := make(map[string]backend.PackageInfo, len(infos))
	for _, info := range infos {
		desired[packageObjectName(repo.Name, info.Type, info.Name)] = info
	}

	// Delete stale.
	for name, pkg := range existing {
		if _, keep := desired[name]; !keep {
			if err := c.Delete(ctx, &pkg); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete stale package %q: %w", name, err)
			}
		}
	}

	// Create or update.
	for name, info := range desired {
		if cur, ok := existing[name]; ok {
			if err := r.updateStatus(ctx, c, &cur, info); err != nil {
				return err
			}
			continue
		}
		if err := r.create(ctx, c, repo, name, info); err != nil {
			return err
		}
	}
	return nil
}

// create makes a new Package owned by repo and writes its observed status.
func (r *Reconciler) create(ctx context.Context, c client.Client, repo *codev1alpha1.Repository, name string, info backend.PackageInfo) error {
	pkg := &codev1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{codev1alpha1.LabelRepository: repo.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         codev1alpha1.SchemeGroupVersion.String(),
				Kind:               "Repository",
				Name:               repo.Name,
				UID:                repo.UID,
				Controller:         ptrBool(true),
				BlockOwnerDeletion: ptrBool(true),
			}},
		},
		Spec: codev1alpha1.PackageSpec{RepositoryRef: repo.Name},
	}
	if err := c.Create(ctx, pkg); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Raced with a previous pass; fetch and fall through to status update.
			if gerr := c.Get(ctx, client.ObjectKey{Name: name}, pkg); gerr != nil {
				return gerr
			}
		} else {
			return fmt.Errorf("create package %q: %w", name, err)
		}
	}
	return r.updateStatus(ctx, c, pkg, info)
}

// updateStatus writes info onto pkg.status, skipping the API call when nothing
// observable changed (avoids churn on every crawl).
func (r *Reconciler) updateStatus(ctx context.Context, c client.Client, pkg *codev1alpha1.Package, info backend.PackageInfo) error {
	now := metav1.Now()
	next := codev1alpha1.PackageStatus{
		ObservedGeneration: pkg.Generation,
		PackageName:        info.Name,
		Type:               info.Type,
		Visibility:         info.Visibility,
		HTMLURL:            info.HTMLURL,
		VersionCount:       info.VersionCount,
		UpdatedAt:          info.UpdatedAt,
		LastSyncTime:       &now,
		Conditions:         pkg.Status.Conditions,
	}
	shared.SetCondition(&next.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "", pkg.Generation)
	if packageStatusEqual(pkg.Status, next) {
		return nil
	}
	pkg.Status = next
	if err := c.Status().Update(ctx, pkg); err != nil {
		return fmt.Errorf("update package %q status: %w", pkg.Name, err)
	}
	return nil
}

// deleteAll removes every Package owned by repo (used when the backend can't
// list packages, so none should exist).
func (r *Reconciler) deleteAll(ctx context.Context, c client.Client, repo *codev1alpha1.Repository) error {
	existing, err := r.listOwned(ctx, c, repo)
	if err != nil {
		return err
	}
	for name, pkg := range existing {
		if err := c.Delete(ctx, &pkg); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete package %q: %w", name, err)
		}
	}
	return nil
}

// listOwned returns the Packages labelled for repo, keyed by object name.
func (r *Reconciler) listOwned(ctx context.Context, c client.Client, repo *codev1alpha1.Repository) (map[string]codev1alpha1.Package, error) {
	var list codev1alpha1.PackageList
	if err := c.List(ctx, &list, client.MatchingLabels{codev1alpha1.LabelRepository: repo.Name}); err != nil {
		return nil, fmt.Errorf("list packages for repository %q: %w", repo.Name, err)
	}
	out := make(map[string]codev1alpha1.Package, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].Name] = list.Items[i]
	}
	return out, nil
}

// packageStatusEqual compares the observable fields of two statuses, ignoring
// LastSyncTime and condition timestamps (which would otherwise force a write
// on every crawl).
func packageStatusEqual(a, b codev1alpha1.PackageStatus) bool {
	return a.PackageName == b.PackageName &&
		a.Type == b.Type &&
		a.Visibility == b.Visibility &&
		a.HTMLURL == b.HTMLURL &&
		a.VersionCount == b.VersionCount &&
		a.UpdatedAt == b.UpdatedAt &&
		a.ObservedGeneration == b.ObservedGeneration &&
		apimeta.IsStatusConditionTrue(a.Conditions, codev1alpha1.ConditionReady)
}

// packageObjectName builds a deterministic, RFC1123-safe object name for a
// package from (repo, type, host name). A short content hash keeps it unique
// and bounded even when the sanitised parts collide or exceed the 253-char
// limit.
func packageObjectName(repo, pkgType, pkgName string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + pkgType + "\x00" + pkgName))
	suffix := hex.EncodeToString(sum[:])[:10]
	base := sanitize(repo + "-" + pkgType + "-" + pkgName)
	const maxBase = 253 - 1 - 10 // leave room for "-" + suffix
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return base + "-" + suffix
}

// sanitize lower-cases and replaces any character outside [a-z0-9-] with '-',
// collapsing the result into a valid DNS-1123 subdomain fragment.
func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// jitter spreads re-crawls of different repositories across the interval so a
// workspace full of repos doesn't stampede the host on the same tick. The
// offset is derived from the repo UID (deterministic, no Math.random needed).
func jitter(base time.Duration, repo *codev1alpha1.Repository) time.Duration {
	if base <= 0 {
		return base
	}
	sum := sha256.Sum256([]byte(repo.UID))
	// up to +25% of base, in steps derived from the first hash byte.
	extra := time.Duration(sum[0]) * base / (255 * 4)
	return base + extra
}

func ptrBool(b bool) *bool { return &b }
