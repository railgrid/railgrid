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
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

const projectEinoAssistantReportProgressTool = "report_progress"
const projectEinoAssistantProgressMaxBytes = 600

const projectEinoAssistantReportProgressParameters = `{
  "type": "object",
  "properties": {
    "message": {
      "type": "string",
      "description": "One or two brief, natural sentences for the user describing the meaningful outcome so far and the next direction. Never include tool names, hidden reasoning, raw arguments, raw results, logs, or secrets."
    }
  },
  "required": ["message"],
  "additionalProperties": false
}`

type projectEinoAssistantProgressTool struct {
	req      projectAssistantRunRequest
	runState *projectEinoAssistantRunState
}

func newProjectEinoAssistantProgressTool(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) einotool.BaseTool {
	return projectEinoAssistantProgressTool{req: req, runState: runState}
}

func projectEinoAssistantProgressEnabled(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) bool {
	return req.StreamCallbacks.OnProgress != nil &&
		projectEinoAssistantProgressApplies(req, runState)
}

func (projectEinoAssistantProgressTool) Info(context.Context) (*schema.ToolInfo, error) {
	var parameters jsonschema.Schema
	if err := json.Unmarshal([]byte(projectEinoAssistantReportProgressParameters), &parameters); err != nil {
		return nil, err
	}
	return &schema.ToolInfo{
		Name:        projectEinoAssistantReportProgressTool,
		Desc:        "Show a brief model-authored progress message to the user without ending the current App Studio turn. Use it when App Studio requires a phase update; then continue the work.",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameters),
	}, nil
}

func (t projectEinoAssistantProgressTool) InvokableRun(
	ctx context.Context,
	argumentsInJSON string,
	_ ...einotool.Option,
) (string, error) {
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	arguments, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return `{"status":"rejected","reason":"invalid progress message"}`, nil
	}
	message, _ := projectToolRawString(arguments["message"])
	message, reason := projectEinoAssistantProgressMessage(message)
	if reason != "" {
		return `{"status":"rejected","reason":"` + reason + `"}`, nil
	}
	if t.runState != nil && !t.runState.AcceptProgressMessage(message) {
		return `{"status":"duplicate","reason":"provide a new progress outcome"}`, nil
	}
	if t.req.StreamCallbacks.OnProgress == nil {
		return "", errors.New("assistant progress callback is not configured")
	}
	t.req.StreamCallbacks.OnProgress(message)
	return `{"status":"shown"}`, nil
}

func projectEinoAssistantProgressMessage(raw string) (string, string) {
	message := raw
	message = strings.Join(strings.Fields(message), " ")
	if !utf8.ValidString(message) || strings.IndexFunc(message, unicode.IsControl) >= 0 {
		return "", "progress message contains invalid text"
	}
	if message == "" {
		return "", "progress message is empty"
	}
	message = projectEinoAssistantRedactSerializedCookieValues(message)
	for _, pattern := range projectEinoAssistantSecretPatterns {
		message = pattern.pattern.ReplaceAllString(message, pattern.replacement)
	}
	if len(message) > projectEinoAssistantProgressMaxBytes {
		end := projectEinoAssistantProgressMaxBytes - 3
		for end > 0 && !utf8.ValidString(message[:end]) {
			end--
		}
		message = strings.TrimSpace(message[:end]) + "..."
	}
	return message, ""
}
