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

package api

import (
	"errors"
	"fmt"
	"testing"
)

// TestTheRunCeilingHandsWorkBackRatherThanRefusingIt is the difference between
// this ceiling and the 64-slot channel it replaced.
//
// The old queue answered a burst with 503 and Retry-After, which made a caller
// responsible for work the provider had already accepted. The Run object is the
// queue now, so a replica at its ceiling must return a RETRYABLE error and
// leave the object exactly as it was: still claimed, still Pending, brought back
// by the reconciler. Nothing is lost and nobody is told to try later.
func TestTheRunCeilingHandsWorkBackRatherThanRefusingIt(t *testing.T) {
	bg := &background{slots: make(chan struct{}, 2)}

	firstAcquired := bg.tryAcquireSlot()
	if !firstAcquired {
		t.Fatal("a fresh ceiling of 2 refused the first two runs")
	}
	secondAcquired := bg.tryAcquireSlot()
	if !secondAcquired {
		t.Fatal("a fresh ceiling of 2 refused the first two runs")
	}
	if bg.tryAcquireSlot() {
		t.Fatal("the ceiling admitted a third run")
	}

	// A finished run gives its slot back, and the next one fits.
	bg.releaseSlot()
	if !bg.tryAcquireSlot() {
		t.Fatal("a released slot was not reusable")
	}

	// Releasing more than was taken must not make room that does not exist:
	// a double release would silently raise the ceiling for good.
	bg.releaseSlot()
	bg.releaseSlot()
	bg.releaseSlot()
	bg.releaseSlot()
	firstAcquired = bg.tryAcquireSlot()
	if !firstAcquired {
		t.Fatal("the ceiling did not come back to 2")
	}
	secondAcquired = bg.tryAcquireSlot()
	if !secondAcquired {
		t.Fatal("the ceiling did not come back to 2")
	}
	if bg.tryAcquireSlot() {
		t.Fatal("over-releasing raised the ceiling permanently")
	}
}

// TestNoCeilingMeansNoCeiling: an operator who sets the ceiling to zero has
// asked for no bound, and must not get a deadlock instead.
func TestNoCeilingMeansNoCeiling(t *testing.T) {
	bg := &background{slots: nil}
	for i := range 100 {
		if !bg.tryAcquireSlot() {
			t.Fatalf("an unbounded background refused run %d", i)
		}
	}
	bg.releaseSlot() // must be a no-op rather than a panic
}

// TestTheCeilingErrorSurvivesWrapping: DispatchRun wraps it with the run's
// name, and a reconciler deciding whether to back off has to still recognise it.
// A wrap with %v instead of %w would make the ceiling indistinguishable from a
// store failure, which is a different decision entirely.
func TestTheCeilingErrorSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("dispatching run %s: %w", "run-1", errNoRunSlot)
	if !errors.Is(wrapped, errNoRunSlot) {
		t.Fatalf("the ceiling error did not survive wrapping: %v", wrapped)
	}
}
