// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import "github.com/railgrid/provider-agents/store"

// publicTranscriptMessages removes the opaque model continuation state before
// transcript rows are returned to a chat client. It leaves the structured tool
// calls and their presentation metadata available to the portal.
func publicTranscriptMessages(messages []store.Message) []store.Message {
	out := append([]store.Message(nil), messages...)
	for index := range out {
		if out[index].Metadata == nil {
			continue
		}
		if _, ok := out[index].Metadata[historyResponsesContinuationKey]; !ok {
			continue
		}
		metadata := make(map[string]any, len(out[index].Metadata)-1)
		for key, value := range out[index].Metadata {
			if key != historyResponsesContinuationKey {
				metadata[key] = value
			}
		}
		if len(metadata) == 0 {
			out[index].Metadata = nil
		} else {
			out[index].Metadata = metadata
		}
	}
	return out
}
