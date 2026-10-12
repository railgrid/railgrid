// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"strings"
	"testing"
)

// Each firing of a trigger gets its own session. The platform's delivery id
// names it when the sender gave one, so a redelivery of the same event lands
// in the same session; a firing with no id gets a fresh one every time.
func TestTriggerSessionIsPerDelivery(t *testing.T) {
	if got := triggerSessionID("on-pr", "  3847668909549314000 "); got != "trigger:on-pr/3847668909549314000" {
		t.Fatalf("session with a delivery id = %q", got)
	}
	first, second := triggerSessionID("on-pr", ""), triggerSessionID("on-pr", "")
	if first == second {
		t.Fatalf("two firings without a delivery id share session %q", first)
	}
	for _, id := range []string{first, second} {
		if !strings.HasPrefix(id, "trigger:on-pr/") || len(id) <= len("trigger:on-pr/") {
			t.Fatalf("session %q is not trigger:<name>/<id>", id)
		}
	}
}
