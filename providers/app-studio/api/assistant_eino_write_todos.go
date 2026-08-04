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

package api

import (
	"context"
	"encoding/json"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
)

const projectEinoAssistantWriteTodosDescription = `Use this tool to create and manage a structured task list for the current coding session. Keep it current throughout non-trivial work: mark exactly one step in_progress before starting it, mark finished steps completed before moving to the next step, and mark every step completed when the work is done.`

var projectEinoAssistantWriteTodosParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "minItems": 1,
      "maxItems": 50,
      "description": "The complete current task list.",
      "items": {
        "type": "object",
        "properties": {
          "content": {"type": "string", "minLength": 1},
          "activeForm": {"type": "string", "minLength": 1},
          "status": {"type": "string", "enum": ["pending", "in_progress", "completed"]}
        },
        "required": ["content", "activeForm", "status"],
        "additionalProperties": false
      }
    }
  },
  "required": ["todos"],
  "additionalProperties": false
}`)

// newProjectEinoAssistantWriteTodosTool is the App Studio-owned replacement
// for Eino Deep's framework middleware. Keeping it as an ordinary App tool
// means every update enters the same request/admission/result ledger as the
// other tools; the lifecycle projects its settled plan only after that result
// is durable.
func newProjectEinoAssistantWriteTodosTool(
	server *Server,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) einotool.BaseTool {
	return projectEinoAssistantTool{
		server:   server,
		req:      req,
		runState: runState,
		tool: projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectEinoAssistantWriteTodosTool,
				Description: projectEinoAssistantWriteTodosDescription,
				Parameters:  append(json.RawMessage(nil), projectEinoAssistantWriteTodosParameters...),
				Risk:        projectAssistantToolRiskPlan,
			},
			call: func(_ context.Context, call projectAssistantToolCallRequest) (string, error) {
				arguments, err := json.Marshal(call.Arguments)
				if err != nil {
					return "", fmt.Errorf("encode write_todos arguments: %w", err)
				}
				plan, err := projectEinoAssistantPlanProgressFromWriteTodos(string(arguments))
				if err != nil {
					return "", err
				}
				todos, err := json.Marshal(plan.Steps)
				if err != nil {
					return "", fmt.Errorf("encode write_todos result: %w", err)
				}
				return fmt.Sprintf("Updated todo list to %s", todos), nil
			},
		},
	}
}
