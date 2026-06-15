/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package repositorycommit

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBundleArrivalTimedOut(t *testing.T) {
	now := time.Unix(100, 0)
	if bundleArrivalTimedOut(nil, now) {
		t.Fatal("nil start unexpectedly timed out")
	}
	recent := metav1.NewTime(now.Add(-bundleArrivalTimeout + time.Second))
	if bundleArrivalTimedOut(&recent, now) {
		t.Fatal("recent start unexpectedly timed out")
	}
	old := metav1.NewTime(now.Add(-bundleArrivalTimeout))
	if !bundleArrivalTimedOut(&old, now) {
		t.Fatal("old start did not time out")
	}
}
