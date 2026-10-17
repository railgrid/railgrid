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

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Ask the installed server rather than maintaining a stale model/effort table.
func validateModelEffort(ctx context.Context, conn *rpcConn, state *runState, model, effort string) error {
	params := map[string]any{"includeHidden": true, "limit": 100}
	seen := map[string]bool{}
	for {
		response, err := conn.call(ctx, "model/list", params, state.handle)
		if err != nil {
			return fmt.Errorf("cannot validate reasoningEffort against Codex models: %w", err)
		}
		var page struct {
			Data []struct {
				ID        string `json:"id"`
				Model     string `json:"model"`
				Supported []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(response.Result, &page); err != nil {
			return fmt.Errorf("decoding Codex model catalog: %w", err)
		}
		for _, entry := range page.Data {
			if entry.Model != model && entry.ID != model {
				continue
			}
			supported := make([]string, 0, len(entry.Supported))
			for _, option := range entry.Supported {
				if option.Effort == effort {
					return nil
				}
				supported = append(supported, option.Effort)
			}
			return fmt.Errorf("reasoningEffort %q is not supported by Codex model %q (supported: %s)", effort, model, strings.Join(supported, ", "))
		}
		if page.NextCursor == nil || *page.NextCursor == "" || seen[*page.NextCursor] {
			break
		}
		seen[*page.NextCursor] = true
		params["cursor"] = *page.NextCursor
	}
	return fmt.Errorf("cannot validate reasoningEffort %q: Codex model %q is not in the installed server's catalog", effort, model)
}
