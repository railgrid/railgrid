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
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestProjectEinoAssistantSafeErrorText(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		secret      string
		wantContext string
	}{
		{
			name:    "authorization bearer",
			message: "request failed: Authorization: Bearer bearer-super-secret",
			secret:  "bearer-super-secret",
		},
		{
			name:    "authorization basic",
			message: "request failed: Authorization: Basic dXNlcjpwYXNzd29yZA==",
			secret:  "dXNlcjpwYXNzd29yZA==",
		},
		{
			name:    "standalone bearer",
			message: "request failed with Bearer standalone-super-secret",
			secret:  "standalone-super-secret",
		},
		{
			name:    "api key",
			message: "request failed: api_key=api-key-super-secret",
			secret:  "api-key-super-secret",
		},
		{
			name:    "access token",
			message: `request failed: access_token: "access-token-super-secret"`,
			secret:  "access-token-super-secret",
		},
		{
			name:    "token",
			message: "request failed: token=token-super-secret",
			secret:  "token-super-secret",
		},
		{
			name:    "secret",
			message: "request failed: secret: secret-super-secret",
			secret:  "secret-super-secret",
		},
		{
			name:    "password",
			message: "request failed: password='password-super-secret'",
			secret:  "password-super-secret",
		},
		{
			name:    "cookie",
			message: "request failed: Cookie: session=cookie-super-secret; theme=dark",
			secret:  "cookie-super-secret",
		},
		{
			name:    "set cookie",
			message: "request failed: Set-Cookie: session=set-cookie-super-secret; Secure",
			secret:  "set-cookie-super-secret",
		},
		{
			name:    "url userinfo",
			message: "request failed: https://demo:url-userinfo-super-secret@example.com/private",
			secret:  "url-userinfo-super-secret",
		},
		{
			name:    "openai key",
			message: "request failed for sk-openai-super-secret",
			secret:  "sk-openai-super-secret",
		},
		{
			name:    "escaped json token",
			message: `request failed: {\"token\":\"escaped-token-super-secret\"}`,
			secret:  "escaped-token-super-secret",
		},
		{
			name:    "escaped json api key",
			message: `request failed: {\"api_key\":\"escaped-api-key-super-secret\"}`,
			secret:  "escaped-api-key-super-secret",
		},
		{
			name:    "json authorization basic",
			message: `request failed: {"Authorization":"Basic dXNlcjpwYXNzd29yZA=="}`,
			secret:  "dXNlcjpwYXNzd29yZA==",
		},
		{
			name:    "escaped json authorization basic",
			message: `request failed: {\"Authorization\":\"Basic ZXNjYXBlZC1zZWNyZXQ=\"}`,
			secret:  "ZXNjYXBlZC1zZWNyZXQ=",
		},
		{
			name:        "json cookie",
			message:     `request failed: {"Cookie":"session=json-cookie-super-secret; theme=dark","status":500}`,
			secret:      "json-cookie-super-secret",
			wantContext: `"status":500`,
		},
		{
			name:        "escaped json cookie",
			message:     `request failed: {\"Cookie\":\"session=escaped-json-cookie-super-secret; theme=dark\",\"status\":500}`,
			secret:      "escaped-json-cookie-super-secret",
			wantContext: `\"status\":500`,
		},
		{
			name:        "json set cookie",
			message:     `request failed: {"Set-Cookie":"session=json-set-cookie-super-secret; Secure","status":500}`,
			secret:      "json-set-cookie-super-secret",
			wantContext: `"status":500`,
		},
		{
			name:        "escaped json set cookie",
			message:     `request failed: {\"Set-Cookie\":\"session=escaped-json-set-cookie-super-secret; Secure\",\"status\":500}`,
			secret:      "escaped-json-set-cookie-super-secret",
			wantContext: `\"status\":500`,
		},
		{
			name:        "json cookie with escaped component",
			message:     `request failed: {"Cookie":"flavor=\"chocolate\"; session=quoted-cookie-super-secret","status":500}`,
			secret:      "quoted-cookie-super-secret",
			wantContext: `"status":500`,
		},
		{
			name:        "escaped json cookie with escaped component",
			message:     `request failed: {\"Cookie\":\"flavor=\\\"chocolate\\\"; session=escaped-quoted-cookie-super-secret\",\"status\":500}`,
			secret:      "escaped-quoted-cookie-super-secret",
			wantContext: `\"status\":500`,
		},
		{
			name:        "json cookie with escaped component before comma",
			message:     `request failed: {"Cookie":"flavor=\"chocolate\", session=ordinary-tail-super-secret","status":500}`,
			secret:      "ordinary-tail-super-secret",
			wantContext: `"status":500`,
		},
		{
			name:        "escaped json cookie with escaped component before comma",
			message:     `request failed: {\"Cookie\":\"flavor=\\\"chocolate\\\", session=tail-super-secret\",\"status\":500}`,
			secret:      "tail-super-secret",
			wantContext: `\"status\":500`,
		},
		{
			name:    "malformed json cookie without true close",
			message: `request failed: {"Cookie":"flavor="chocolate", session=malformed-tail-super-secret`,
			secret:  "malformed-tail-super-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(tt.message + " " + strings.Repeat("x", projectToolInfoLimit))
			got := projectEinoAssistantSafeErrorText(err)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("safe error = %q, still contains secret %q", got, tt.secret)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("safe error = %q, want redaction marker", got)
			}
			if tt.wantContext != "" && !strings.Contains(got, tt.wantContext) {
				t.Fatalf("safe error = %q, want unrelated context %q preserved", got, tt.wantContext)
			}
			if got != truncateProjectToolInfo(got) {
				t.Fatalf("safe error length = %d, want bounded by truncateProjectToolInfo", len(got))
			}
		})
	}
}

func TestProjectEinoAssistantSafeErrorTextRedactsMultipleSerializedCookies(t *testing.T) {
	err := errors.New(
		`request failed: {"Cookie":"session=first-cookie-super-secret","status":500}` +
			` and {\"Set-Cookie\":\"session=second-cookie-super-secret\",\"status\":200}`,
	)
	got := projectEinoAssistantSafeErrorText(err)
	for _, secret := range []string{"first-cookie-super-secret", "second-cookie-super-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safe error = %q, still contains secret %q", got, secret)
		}
	}
	if strings.Count(got, "[REDACTED]") != 2 {
		t.Fatalf("safe error = %q, want two redaction markers", got)
	}
	for _, context := range []string{`"status":500`, `\"status\":200`} {
		if !strings.Contains(got, context) {
			t.Fatalf("safe error = %q, want unrelated context %q preserved", got, context)
		}
	}
}

func TestProjectEinoAssistantPhaseProgressReminderAllowsDirectOperationalAction(t *testing.T) {
	for _, phase := range []projectEinoAssistantPhase{
		projectEinoAssistantPhaseApproval,
		projectEinoAssistantPhaseMutate,
	} {
		reminder := projectEinoAssistantPhaseProgressReminder(phase)
		if !strings.Contains(reminder, "direct runtime or infrastructure action") {
			t.Fatalf("%s reminder = %q, want direct operational-action guidance", phase, reminder)
		}
	}
}

func TestProjectEinoAssistantSafeToolErrorMiddleware(t *testing.T) {
	middleware := &projectEinoAssistantSafeToolErrorMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}

	t.Run("invokable", func(t *testing.T) {
		endpoint, err := middleware.WrapInvokableToolCall(
			context.Background(),
			func(context.Context, string, ...einotool.Option) (string, error) {
				return "", errors.New(
					"backend failed: token=invokable-super-secret " +
						strings.Repeat("x", projectToolInfoLimit),
				)
			},
			&adk.ToolContext{Name: "invokable"},
		)
		if err != nil {
			t.Fatalf("wrap invokable tool: %v", err)
		}
		result, err := endpoint(context.Background(), "{}")
		if err != nil {
			t.Fatalf("invoke wrapped tool: %v", err)
		}
		if strings.Contains(result, "invokable-super-secret") ||
			!strings.Contains(result, "token=[REDACTED]") ||
			!strings.HasPrefix(result, "Tool call failed: ") {
			t.Fatalf("result = %q, want sanitized tool failure", result)
		}
		if result != truncateProjectToolInfo(result) {
			t.Fatalf("result length = %d, want bounded by truncateProjectToolInfo", len(result))
		}
	})

	t.Run("enhanced invokable", func(t *testing.T) {
		endpoint, err := middleware.WrapEnhancedInvokableToolCall(
			context.Background(),
			func(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
				return nil, errors.New(
					"backend failed: secret=enhanced-super-secret " +
						strings.Repeat("x", projectToolInfoLimit),
				)
			},
			&adk.ToolContext{Name: "enhanced"},
		)
		if err != nil {
			t.Fatalf("wrap enhanced invokable tool: %v", err)
		}
		result, err := endpoint(context.Background(), &schema.ToolArgument{Text: "{}"})
		if err != nil {
			t.Fatalf("invoke wrapped enhanced tool: %v", err)
		}
		if result == nil || len(result.Parts) != 1 || result.Parts[0].Type != schema.ToolPartTypeText {
			t.Fatalf("result = %#v, want one sanitized text part", result)
		}
		text := result.Parts[0].Text
		if strings.Contains(text, "enhanced-super-secret") ||
			!strings.Contains(text, "secret=[REDACTED]") ||
			!strings.HasPrefix(text, "Tool call failed: ") {
			t.Fatalf("result text = %q, want sanitized tool failure", text)
		}
		if text != truncateProjectToolInfo(text) {
			t.Fatalf("result text length = %d, want bounded by truncateProjectToolInfo", len(text))
		}
	})
}

func TestProjectEinoAssistantSafeToolErrorMiddlewarePropagatesControlFlow(t *testing.T) {
	interruptErr := einotool.StatefulInterrupt(
		context.Background(),
		"approval required",
		map[string]string{"status": "waiting"},
	)
	if _, ok := compose.IsInterruptRerunError(interruptErr); !ok {
		t.Fatalf("stateful interrupt = %v, want real Eino interrupt/rerun error", interruptErr)
	}

	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "context deadline exceeded", err: context.DeadlineExceeded},
		{name: "stream canceled", err: adk.ErrStreamCanceled},
		{name: "forbidden", err: apierrors.NewForbidden(
			k8sschema.GroupResource{Group: "ai.kedge.faros.sh", Resource: "projects"},
			"demo",
			errors.New("denied"),
		)},
		{name: "unauthorized", err: apierrors.NewUnauthorized("denied")},
		{name: "plan retirement", err: errProjectAssistantPlanRetirement},
		{name: "plan grant persistence", err: errProjectAssistantPlanGrantPersistence},
		{name: "stateful interrupt", err: interruptErr},
	}

	middleware := &projectEinoAssistantSafeToolErrorMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invokable, err := middleware.WrapInvokableToolCall(
				context.Background(),
				func(context.Context, string, ...einotool.Option) (string, error) {
					return "", tt.err
				},
				&adk.ToolContext{Name: "invokable"},
			)
			if err != nil {
				t.Fatalf("wrap invokable tool: %v", err)
			}
			if _, gotErr := invokable(context.Background(), "{}"); gotErr != tt.err {
				t.Fatalf("invokable error = %v, want original error %v", gotErr, tt.err)
			}

			enhanced, err := middleware.WrapEnhancedInvokableToolCall(
				context.Background(),
				func(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
					return nil, tt.err
				},
				&adk.ToolContext{Name: "enhanced"},
			)
			if err != nil {
				t.Fatalf("wrap enhanced invokable tool: %v", err)
			}
			if _, gotErr := enhanced(context.Background(), &schema.ToolArgument{Text: "{}"}); gotErr != tt.err {
				t.Fatalf("enhanced error = %v, want original error %v", gotErr, tt.err)
			}
		})
	}
}

func TestProjectEinoAssistantShouldRetryModelError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"openai 429", &openaimodel.APIError{HTTPStatusCode: 429}, true},
		{"openai 503", &openaimodel.APIError{HTTPStatusCode: 503}, true},
		{"openai 400", &openaimodel.APIError{HTTPStatusCode: 400}, false},
		{"openai 401", &openaimodel.APIError{HTTPStatusCode: 401}, false},
		{"gemini 429", genai.APIError{Code: 429}, true},
		{"gemini 503", genai.APIError{Code: 503}, true},
		{"gemini 403", genai.APIError{Code: 403}, false},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"generic", errors.New("provider failed"), false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEinoAssistantShouldRetryModelError(tt.err); got != tt.want {
				t.Fatalf("projectEinoAssistantShouldRetryModelError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantModelRetryConfig(t *testing.T) {
	config := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileImplementation,
	}, newProjectEinoAssistantRunState())
	if config.MaxRetries != 2 {
		t.Fatalf("MaxRetries = %d, want 2", config.MaxRetries)
	}

	input := []*schema.Message{schema.UserMessage("Build the app")}
	tests := []struct {
		name         string
		req          projectAssistantRunRequest
		retryCtx     *adk.RetryContext
		wantRetry    bool
		wantReason   any
		wantReminder bool
		wantBackoff  time.Duration
		canceled     bool
	}{
		{
			name: "approval prose retries for required phase progress",
			retryCtx: &adk.RetryContext{
				RetryAttempt:  1,
				InputMessages: input,
				OutputMessage: schema.AssistantMessage("I have reviewed the requested work.", nil),
			},
			wantRetry:    true,
			wantReason:   "incomplete phase progress: approval",
			wantReminder: true,
			wantBackoff:  -time.Nanosecond,
		},
		{
			name: "tool call output is accepted",
			retryCtx: &adk.RetryContext{
				RetryAttempt:  1,
				InputMessages: input,
				OutputMessage: schema.AssistantMessage("", []schema.ToolCall{{
					ID:       "call-plan",
					Function: schema.FunctionCall{Name: projectToolRequestProjectPlanApproval},
				}}),
			},
		},
		{
			name: "discussion prose is accepted",
			req: projectAssistantRunRequest{
				TurnProfile: projectAssistantTurnProfileDiscussion,
			},
			retryCtx: &adk.RetryContext{
				RetryAttempt:  1,
				InputMessages: input,
				OutputMessage: schema.AssistantMessage("Here is the design tradeoff.", nil),
			},
		},
		{
			name: "report prose is accepted",
			retryCtx: &adk.RetryContext{
				RetryAttempt: 1,
				InputMessages: append(append([]*schema.Message{}, input...), schema.ToolMessage(
					"committed",
					"call-commit",
					schema.WithToolName(projectToolCommitProjectFiles),
				)),
				OutputMessage: schema.AssistantMessage("The implementation is complete.", nil),
			},
		},
		{
			name: "second semantic retry attempt is rejected until eino exhausts retries",
			retryCtx: &adk.RetryContext{
				RetryAttempt:  2,
				InputMessages: input,
				OutputMessage: schema.AssistantMessage("I have reviewed the requested work.", nil),
			},
			wantRetry:    true,
			wantReason:   "incomplete phase progress: approval",
			wantReminder: true,
			wantBackoff:  -time.Nanosecond,
		},
		{
			name: "transient provider error remains retryable",
			retryCtx: &adk.RetryContext{
				RetryAttempt: 1,
				Err:          io.ErrUnexpectedEOF,
			},
			wantRetry:  true,
			wantReason: "transient model provider failure",
			// Zero preserves Eino's configured/default transient-error delay.
			wantBackoff: 0,
		},
		{
			name: "permanent provider error is accepted",
			retryCtx: &adk.RetryContext{
				RetryAttempt: 1,
				Err:          errors.New("provider failed"),
			},
		},
		{
			name: "canceled context is accepted",
			retryCtx: &adk.RetryContext{
				RetryAttempt: 1,
				Err:          io.ErrUnexpectedEOF,
			},
			canceled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			if req.TurnProfile == "" {
				req.TurnProfile = projectAssistantTurnProfileImplementation
			}
			config := projectEinoAssistantModelRetryConfig(req, newProjectEinoAssistantRunState())
			ctx := context.Background()
			if tt.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			decision := config.ShouldRetry(ctx, tt.retryCtx)
			if decision.Retry != tt.wantRetry {
				t.Fatalf("Retry = %t, want %t (decision %#v)", decision.Retry, tt.wantRetry, decision)
			}
			if decision.RejectReason != tt.wantReason {
				t.Fatalf("RejectReason = %#v, want %#v", decision.RejectReason, tt.wantReason)
			}
			if decision.Backoff != tt.wantBackoff {
				t.Fatalf("Backoff = %s, want %s", decision.Backoff, tt.wantBackoff)
			}
			if !tt.wantReminder {
				if decision.ModifiedInputMessages != nil || decision.PersistModifiedInputMessages || len(decision.AdditionalOptions) != 0 {
					t.Fatalf("non-semantic decision = %#v, want no modified input or options", decision)
				}
				return
			}

			if len(decision.ModifiedInputMessages) != len(input)+1 {
				t.Fatalf("ModifiedInputMessages = %#v, want original input plus reminder", decision.ModifiedInputMessages)
			}
			for i := range input {
				if decision.ModifiedInputMessages[i] != input[i] {
					t.Fatalf("ModifiedInputMessages[%d] = %#v, want original input %#v", i, decision.ModifiedInputMessages[i], input[i])
				}
			}
			reminder := decision.ModifiedInputMessages[len(input)]
			if reminder.Role != schema.System || !strings.Contains(reminder.Content, "approval") || !strings.Contains(reminder.Content, projectToolRequestProjectPlanApproval) {
				t.Fatalf("reminder = %#v, want phase-specific approval action", reminder)
			}
			if decision.PersistModifiedInputMessages {
				t.Fatalf("PersistModifiedInputMessages = true, want false")
			}
			options := einomodel.GetCommonOptions(nil, decision.AdditionalOptions...)
			if options.ToolChoice == nil || *options.ToolChoice != schema.ToolChoiceForced {
				t.Fatalf("AdditionalOptions = %#v, want forced tool choice", decision.AdditionalOptions)
			}
		})
	}
}

func TestProjectEinoAssistantPatchToolCallsMarksCompletionUnknown(t *testing.T) {
	middleware, err := projectEinoAssistantPatchToolCallsMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create recovery middleware: %v", err)
	}
	_, state, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: []adk.Message{
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:   "call-write-file",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolWriteFile,
					Arguments: `{"path":"src/App.tsx","content":"updated"}`,
				},
			}}),
		}},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatalf("rewrite dangling tool call: %v", err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("messages = %#v, want assistant call and patched tool result", state.Messages)
	}
	patched := state.Messages[1]
	if patched.Role != schema.Tool || patched.ToolCallID != "call-write-file" {
		t.Fatalf("patched message = %#v, want tool result for dangling call", patched)
	}
	if !strings.Contains(patched.Content, "completion is unknown") || !strings.Contains(patched.Content, "inspect current") {
		t.Fatalf("patched content = %q, want completion uncertainty and inspection guidance", patched.Content)
	}
}
