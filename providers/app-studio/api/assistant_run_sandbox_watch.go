/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"

	asclient "github.com/railgrid/provider-app-studio/client"
)

// runSandboxInstanceCondition judges one observation of the instance. obj
// is nil (and deleted true) when the instance does not exist. done ends the
// wait successfully; an error ends it with that error.
type runSandboxInstanceCondition func(obj *unstructured.Unstructured, deleted bool) (done bool, err error)

// runSandboxInstanceGetError marks a failed initial read, which the callers
// report as such rather than as a timeout.
type runSandboxInstanceGetError struct{ err error }

func (e *runSandboxInstanceGetError) Error() string { return e.err.Error() }
func (e *runSandboxInstanceGetError) Unwrap() error { return e.err }

// runSandboxWatchRetryDelay spaces re-reads when a watch cannot be opened
// (a proxy that does not stream, a transient failure) — the wait then
// degrades to a slow poll instead of hanging until its timeout.
const runSandboxWatchRetryDelay = time.Second

// watchRunSandboxInstance follows one instance by name — an initial read,
// then a watch narrowed to that name from the read's resourceVersion — and
// hands every observation to condition until it reports done, errors, or
// ctx ends. A watch that closes or expires is reopened after a fresh read
// (whose observation is judged too), so no transition is missed between
// streams. It returns ctx's error when the context ends first.
func watchRunSandboxInstance(ctx context.Context, rc asclient.ResourceClient, name string, condition runSandboxInstanceCondition) error {
	if rc == nil {
		return errors.New("project client is not configured")
	}
	if condition == nil {
		return errors.New("run sandbox instance condition is not configured")
	}
	observe := func(initial bool) (resourceVersion string, done bool, err error) {
		obj, err := rc.Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			done, err = condition(nil, true)
			return "", done, err
		case err != nil:
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			if initial {
				return "", false, &runSandboxInstanceGetError{err: err}
			}
			return "", false, nil
		}
		done, err = condition(obj, false)
		return obj.GetResourceVersion(), done, err
	}
	pause := func() {
		timer := time.NewTimer(runSandboxWatchRetryDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}

	resourceVersion, done, err := observe(true)
	if done || err != nil {
		return err
	}
	selector := fields.OneTermEqualSelector("metadata.name", name).String()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wi, err := rc.Watch(ctx, metav1.ListOptions{FieldSelector: selector, ResourceVersion: resourceVersion, AllowWatchBookmarks: true})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !apierrors.IsResourceExpired(err) && !apierrors.IsGone(err) {
				pause()
			}
		} else {
			done, err, lastSeen := followRunSandboxInstance(ctx, wi, name, condition)
			wi.Stop()
			if done || err != nil {
				return err
			}
			if lastSeen != "" {
				resourceVersion = lastSeen
			}
		}
		// The stream ended without a verdict: re-read so a transition that
		// happened while no watch was open is judged before the next one.
		if resourceVersion, done, err = observe(false); done || err != nil {
			return err
		}
	}
}

// followRunSandboxInstance consumes one watch stream. It returns the
// condition's verdict, or (false, nil) when the stream ends without one,
// along with the last resourceVersion observed.
func followRunSandboxInstance(ctx context.Context, wi watch.Interface, name string, condition runSandboxInstanceCondition) (done bool, err error, lastSeen string) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err(), lastSeen
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return false, nil, lastSeen
			}
			obj, isObject := ev.Object.(*unstructured.Unstructured)
			if isObject && obj.GetResourceVersion() != "" {
				lastSeen = obj.GetResourceVersion()
			}
			switch ev.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				if !isObject || obj.GetName() != name {
					continue
				}
				if done, err := condition(obj, ev.Type == watch.Deleted); done || err != nil {
					return done, err, lastSeen
				}
			case watch.Error:
				// Expired or otherwise broken: the caller re-reads and reopens.
				return false, nil, lastSeen
			}
		}
	}
}

// waitForProjectAssistantRunSandboxInstanceReady blocks until the named
// instance satisfies projectAssistantRunSandboxInstanceReadiness, its status
// turns terminal, or timeout elapses. The instance is watched, not polled;
// the returned errors are the readiness contract callers and tests rely on.
func waitForProjectAssistantRunSandboxInstanceReady(ctx context.Context, timeout time.Duration, components map[string]projectTemplateComponent, name string, rc asclient.ResourceClient) error {
	if rc == nil {
		return errors.New("project client is not configured")
	}
	if timeout <= 0 {
		timeout = projectAssistantRunSandboxReadyTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastReason := "instance is not ready"
	err := watchRunSandboxInstance(waitCtx, rc, name, func(obj *unstructured.Unstructured, deleted bool) (bool, error) {
		if deleted || obj == nil {
			lastReason = "instance has not been observed"
			return false, nil
		}
		ready, terminal, reason := projectAssistantRunSandboxInstanceReadiness(obj, components)
		if ready {
			return true, nil
		}
		if terminal {
			return false, fmt.Errorf("instance is not ready: %s", reason)
		}
		if reason != "" {
			lastReason = reason
		}
		return false, nil
	})
	var getErr *runSandboxInstanceGetError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("wait for run sandbox instance: %w", ctx.Err())
	case errors.As(err, &getErr):
		return fmt.Errorf("get run sandbox instance status: %w", getErr.err)
	case waitCtx.Err() != nil && errors.Is(err, waitCtx.Err()):
		return fmt.Errorf("instance did not become ready within %s: %s", timeout, lastReason)
	default:
		return err
	}
}

// waitForProjectAssistantRunSandboxInstanceDeleted blocks until the named
// instance is gone, or the readiness timeout elapses. Watched, not polled.
func waitForProjectAssistantRunSandboxInstanceDeleted(ctx context.Context, c *asclient.Client, name string) error {
	if c == nil {
		return errors.New("project client is not configured")
	}
	waitCtx, cancel := context.WithTimeout(ctx, projectAssistantRunSandboxReadyTimeout)
	defer cancel()
	err := watchRunSandboxInstance(waitCtx, c.Resource(runSandboxInstancesResource, ""), name, func(obj *unstructured.Unstructured, deleted bool) (bool, error) {
		return deleted || obj == nil, nil
	})
	var getErr *runSandboxInstanceGetError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &getErr):
		return fmt.Errorf("get deleting run sandbox instance %q: %w", name, getErr.err)
	case waitCtx.Err() != nil && errors.Is(err, waitCtx.Err()):
		return fmt.Errorf("wait for expired run sandbox instance %q deletion: %w", name, waitCtx.Err())
	default:
		return err
	}
}
