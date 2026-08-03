// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"errors"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantStartReplayIsBoundToV2Request(t *testing.T) {
	run := store.AssistantRun{Mode: store.AssistantRunModeDefault}
	if err := bindProjectAssistantStartRequest(&run, "actor-1", " build it "); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectAssistantStartReplay(run, "actor-1", "build it", store.AssistantRunModeDefault); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	for _, replay := range []struct {
		actor   string
		content string
		mode    store.AssistantRunMode
	}{
		{actor: "actor-2", content: "build it", mode: store.AssistantRunModeDefault},
		{actor: "actor-1", content: "different", mode: store.AssistantRunModeDefault},
		{actor: "actor-1", content: "build it", mode: store.AssistantRunModePlan},
	} {
		if err := validateProjectAssistantStartReplay(run, replay.actor, replay.content, replay.mode); !errors.Is(err, store.ErrAssistantRunConflict) {
			t.Fatalf("changed replay error = %v, want run conflict", err)
		}
	}
}
