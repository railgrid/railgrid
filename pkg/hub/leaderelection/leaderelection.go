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

// Package leaderelection gates the hub's singleton controllers so a hub scaled
// to multiple replicas still reconciles each object exactly once.
//
// The lock is a coordination.k8s.io/v1 Lease held in a kcp workspace (kcp
// serves Leases inside every logical cluster), which keeps the hub's only hard
// dependency kcp — no host-cluster ServiceAccount or RBAC is required, and the
// hub works unchanged when it runs outside Kubernetes.
//
// Only *write* loops belong behind this gate. State that the request path reads
// — most importantly the provider routing registry, which the catalog
// controller keeps in sync — must be maintained on every replica, or a
// non-leader would serve requests against an empty routing table.
package leaderelection

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

// Default lease timings. They match controller-runtime's defaults, which are
// tuned for a control plane that may briefly stall (kcp shard restart, front
// proxy failover) without triggering a spurious failover.
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

// Options configures a single election.
type Options struct {
	// Config must already target the workspace holding the Lease (i.e. its
	// Host carries the /clusters/<path> segment).
	Config *rest.Config
	// Namespace and Name identify the Lease object.
	Namespace string
	Name      string
	// Identity distinguishes this replica. Defaults to the pod hostname plus
	// the process ID, which stays unique if two replicas ever share a host.
	Identity string

	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

func (o *Options) defaults() error {
	if o.Config == nil {
		return fmt.Errorf("leaderelection: Config is required")
	}
	return o.defaultsWithoutConfig()
}

func (o *Options) defaultsWithoutConfig() error {
	if o.Namespace == "" || o.Name == "" {
		return fmt.Errorf("leaderelection: Namespace and Name are required")
	}
	if o.Identity == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("leaderelection: resolving hostname for identity: %w", err)
		}
		o.Identity = fmt.Sprintf("%s_%d", host, os.Getpid())
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = DefaultLeaseDuration
	}
	if o.RenewDeadline <= 0 {
		o.RenewDeadline = DefaultRenewDeadline
	}
	if o.RetryPeriod <= 0 {
		o.RetryPeriod = DefaultRetryPeriod
	}
	return nil
}

// Run campaigns for the lease and calls run every time this replica is elected,
// with a context that is cancelled the moment leadership is lost. It blocks
// until ctx is done, re-entering the election after each loss, so a transient
// kcp hiccup costs the hub a controller pause rather than a process restart —
// the same replica keeps serving API-proxy, portal, and provider traffic
// throughout.
//
// run must return promptly once its context is cancelled: the next leader may
// already be reconciling.
func Run(ctx context.Context, opts Options, run func(context.Context)) error {
	if err := (&opts).defaults(); err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(opts.Config)
	if err != nil {
		return fmt.Errorf("leaderelection: building client: %w", err)
	}
	return runWithClient(ctx, opts, client, run)
}

// runWithClient is Run with the client injected, so tests can drive the whole
// election loop against a fake API.
func runWithClient(ctx context.Context, opts Options, client kubernetes.Interface, run func(context.Context)) error {
	if err := (&opts).defaultsWithoutConfig(); err != nil {
		return err
	}
	logger := klog.FromContext(ctx).WithName("leaderelection").
		WithValues("lease", opts.Namespace+"/"+opts.Name, "identity", opts.Identity)

	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Namespace: opts.Namespace, Name: opts.Name},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: opts.Identity},
	}

	// The election callback only hands the term's context back; run itself is
	// invoked on this goroutine below. client-go launches OnStartedLeading in a
	// goroutine it never joins, so calling run from there would let a new term
	// begin while the previous term's controllers are still draining.
	terms := make(chan context.Context, 1)

	config := leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: opts.LeaseDuration,
		RenewDeadline: opts.RenewDeadline,
		RetryPeriod:   opts.RetryPeriod,
		// Hand the lease back on a clean shutdown so a rolling update promotes
		// the next replica immediately instead of after LeaseDuration.
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(termCtx context.Context) { terms <- termCtx },
			OnStoppedLeading: func() {
				logger.Info("Lost leadership; controllers stopping, will campaign again")
			},
			OnNewLeader: func(identity string) {
				if identity != opts.Identity {
					logger.V(2).Info("Observed new leader", "leader", identity)
				}
			},
		},
	}

	for {
		elector, err := leaderelection.NewLeaderElector(config)
		if err != nil {
			return fmt.Errorf("leaderelection: creating elector: %w", err)
		}
		logger.V(2).Info("Campaigning for leadership")

		// Scoped to this term so we can step down deliberately: if run returns
		// while the lease is still held — a controller failed to start, say —
		// holding the lease would stall the platform, because no other replica
		// can take over work this one is no longer doing. Cancelling here
		// releases the lease and lets a peer win the next round.
		electionCtx, endTerm := context.WithCancel(ctx)
		electorDone := make(chan struct{})
		go func() {
			defer close(electorDone)
			elector.Run(electionCtx)
		}()

		select {
		case termCtx := <-terms:
			logger.Info("Acquired leadership")
			run(termCtx)
			if termCtx.Err() == nil {
				logger.Info("Controllers stopped while still leader; releasing the lease")
			}
			endTerm()
			<-electorDone
		case <-electorDone:
			// Never won this round, or ctx was cancelled while campaigning.
		}
		endTerm()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(opts.RetryPeriod):
		}
	}
}
