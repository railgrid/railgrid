/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

// waitForPhase waits for the named object of gvr to satisfy isTerminal, or
// for timeout to elapse, whichever is first. It reads the object once — an
// already-terminal object returns at once — and then follows a single watch
// scoped to that object by field selector from the resource version it read,
// so no state change between the read and the watch is missed and nothing
// polls. Bookmarks are ignored; a closed watch or an expired resource version
// re-reads and re-watches.
//
// It returns (obj, true, nil) once isTerminal accepts an observed state.
// When the wait ends first — timeout or the caller's context — it returns
// the last state it observed (nil when it saw none), false, and no error, so
// callers can tell "timed out" from a failed request. An error is only ever
// a failure of the read or watch itself, or the object vanishing before it
// finished.
func waitForPhase(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, kind, name string, timeout time.Duration, isTerminal func(*unstructured.Unstructured) bool) (*unstructured.Unstructured, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := dyn.Resource(gvr)
	var last *unstructured.Unstructured
	for {
		obj, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return last, false, nil
			}
			return nil, false, fmt.Errorf("get %s %q: %w", kind, name, err)
		}
		last = obj
		if isTerminal(obj) {
			return obj, true, nil
		}

		w, err := client.Watch(ctx, metav1.ListOptions{
			FieldSelector:       "metadata.name=" + name,
			ResourceVersion:     obj.GetResourceVersion(),
			AllowWatchBookmarks: true,
		})
		if err != nil {
			if ctx.Err() != nil {
				return last, false, nil
			}
			return nil, false, fmt.Errorf("watch %s %q: %w", kind, name, err)
		}
		obj, done, resync, err := followWatch(ctx, w, kind, name, isTerminal)
		if obj != nil {
			last = obj
		}
		switch {
		case err != nil:
			return nil, false, err
		case done:
			return obj, true, nil
		case ctx.Err() != nil || !resync:
			return last, false, nil
		}
		// The watch ended without a verdict: re-read and follow again.
	}
}

// followWatch consumes w until isTerminal accepts an object (done), the
// context ends, or the watch needs re-establishing (resync: the server closed
// it or its resource version expired). It returns the last object it saw
// for name, if any, and stops w. A Deleted event for name is an error: the
// request vanished before it finished.
func followWatch(ctx context.Context, w watch.Interface, kind, name string, isTerminal func(*unstructured.Unstructured) bool) (last *unstructured.Unstructured, done, resync bool, err error) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, false, false, nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return last, false, true, nil
			}
			switch ev.Type {
			case watch.Bookmark:
				continue
			case watch.Error:
				status := apierrors.FromObject(ev.Object)
				if apierrors.IsResourceExpired(status) || apierrors.IsGone(status) {
					return last, false, true, nil
				}
				return last, false, false, fmt.Errorf("watch %s %q: %w", kind, name, status)
			}
			obj, ok := ev.Object.(*unstructured.Unstructured)
			if !ok || obj.GetName() != name {
				// Fakes and older servers may not honour the field selector.
				continue
			}
			if ev.Type == watch.Deleted {
				return last, false, false, fmt.Errorf("%s %q was deleted before it completed", kind, name)
			}
			last = obj
			if isTerminal(obj) {
				return obj, true, false, nil
			}
		}
	}
}

// phaseIn reports whether status.phase is one of phases.
func phaseIn(phases ...string) func(*unstructured.Unstructured) bool {
	return func(obj *unstructured.Unstructured) bool {
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		for _, p := range phases {
			if phase == p {
				return true
			}
		}
		return false
	}
}
