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
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantToolBatchAdmissionPreservesModelCalls(t *testing.T) {
	calls := make([]schema.ToolCall, 133)
	for index := range calls {
		calls[index] = projectEinoAssistantToolCallForAdmissionTest(
			fmt.Sprintf("read-%d", index),
			projectToolReadFile,
			`{"limit":200,"file_path":"src/App.tsx","offset":1}`,
		)
	}

	normalized, err := projectEinoAssistantNormalizeToolBatch(calls, 7)
	if err != nil {
		t.Fatalf("normalize tool batch: %v", err)
	}
	if len(normalized) != len(calls) {
		t.Fatalf("normalized calls = %d, want all %d model calls", len(normalized), len(calls))
	}
	if normalized[0].ID != "read-0" {
		t.Fatalf("call ID = %q, want first model-provided ID", normalized[0].ID)
	}
	if normalized[0].Function.Arguments != `{"file_path":"src/App.tsx","limit":200,"offset":1}` {
		t.Fatalf("arguments = %q, want canonical JSON", normalized[0].Function.Arguments)
	}
}

func TestProjectEinoAssistantToolBatchAdmissionAllowsMixedAndRepeatedActions(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":"a"}`),
		projectEinoAssistantToolCallForAdmissionTest("patch-1", projectToolApplyPatch, `{"path":"a","oldText":"x","newText":"y"}`),
		projectEinoAssistantToolCallForAdmissionTest("patch-2", projectToolApplyPatch, `{"path":"a","oldText":"x","newText":"y"}`),
		projectEinoAssistantToolCallForAdmissionTest("commit", projectToolCommitProjectFiles, `{"paths":["a"]}`),
	}
	normalized, err := projectEinoAssistantNormalizeToolBatch(calls, 2)
	if err != nil {
		t.Fatalf("normalize mixed tool batch: %v", err)
	}
	if len(normalized) != len(calls) {
		t.Fatalf("normalized calls = %d, want all %d model calls", len(normalized), len(calls))
	}
	for index := range normalized {
		if normalized[index].ID != calls[index].ID || normalized[index].Function.Name != calls[index].Function.Name {
			t.Fatalf("normalized call %d = %#v, want model call %#v", index, normalized[index], calls[index])
		}
	}
}

func TestProjectEinoAssistantToolBatchAdmissionRejectsMalformedBatches(t *testing.T) {
	tests := []struct {
		name  string
		calls []schema.ToolCall
		code  string
	}{
		{
			name: "conflicting call ID",
			calls: []schema.ToolCall{
				projectEinoAssistantToolCallForAdmissionTest("same", projectToolReadFile, `{"file_path":"a"}`),
				projectEinoAssistantToolCallForAdmissionTest("same", projectToolReadFile, `{"file_path":"b"}`),
			},
			code: "conflicting_tool_call_id",
		},
		{
			name: "malformed arguments",
			calls: []schema.ToolCall{
				projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":`),
			},
			code: "invalid_tool_arguments",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := projectEinoAssistantNormalizeToolBatch(test.calls, 1)
			var admissionErr *projectEinoAssistantInvalidToolBatchError
			if !errors.As(err, &admissionErr) || admissionErr.Code != test.code {
				t.Fatalf("error = %v, want admission code %q", err, test.code)
			}
		})
	}
}

func TestProjectEinoAssistantToolBatchAdmissionAssignsDeterministicIDs(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("", projectToolReadFile, `{"file_path":"a"}`),
		projectEinoAssistantToolCallForAdmissionTest("", projectToolGrep, `{"pattern":"needle","path":"src"}`),
	}
	first, err := projectEinoAssistantNormalizeToolBatch(calls, 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectEinoAssistantNormalizeToolBatch(calls, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].ID == "" || first[1].ID == "" || first[0].ID == first[1].ID {
		t.Fatalf("generated IDs = %q, %q, want distinct non-empty values", first[0].ID, first[1].ID)
	}
	if first[0].ID != second[0].ID || first[1].ID != second[1].ID {
		t.Fatalf("generated IDs changed for the same admitted batch: %#v vs %#v", first, second)
	}
}

func TestProjectEinoAssistantToolBatchAdmissionDoesNotReuseIDsAfterSummarization(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(runState)
	admit := func() string {
		t.Helper()
		runState.NextModelCallOrdinal()
		call := projectEinoAssistantToolCallForAdmissionTest("", projectToolReadFile, `{"file_path":"src/App.tsx"}`)
		// Summarization may leave no earlier tool batches in Eino's message
		// window. The durable model-call ordinal must still distinguish this
		// accepted call from an identical earlier call in the same run.
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{
			schema.AssistantMessage("condensed prior context", nil),
			schema.UserMessage("inspect again"),
			schema.AssistantMessage("", []schema.ToolCall{call}),
		}}
		_, state, err := middleware.AfterModelRewriteState(context.Background(), state, &adk.ModelContext{})
		if err != nil {
			t.Fatal(err)
		}
		return state.Messages[len(state.Messages)-1].ToolCalls[0].ID
	}

	first := admit()
	second := admit()
	if first == "" || second == "" || first == second {
		t.Fatalf("summarized identical calls received IDs %q and %q, want distinct durable IDs", first, second)
	}
}

func TestProjectEinoAssistantToolBatchMiddlewareReconcilesRunState(t *testing.T) {
	raw := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("read-1", projectToolReadFile, `{"limit":10,"file_path":"src/App.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-2", projectToolReadFile, `{"file_path":"src/App.tsx","limit":10}`),
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordAssistantReply(projectAssistantReply{ToolCalls: projectEinoToolCallsToChat(raw)})
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.UserMessage("inspect"),
		schema.AssistantMessage("", raw),
	}}
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(runState)
	_, state, err := middleware.AfterModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Messages[len(state.Messages)-1].ToolCalls); got != 2 {
		t.Fatalf("state calls = %d, want two", got)
	}
	runState.mu.Lock()
	defer runState.mu.Unlock()
	if got := len(runState.toolCalls); got != 2 {
		t.Fatalf("run-state calls = %d, want two normalized calls", got)
	}
	if len(runState.messages) != 1 || len(runState.messages[0].ToolCalls) != 2 {
		t.Fatalf("run-state messages = %#v, want the normalized assistant batch", runState.messages)
	}
}

func TestProjectEinoAssistantModelRetryConfigDoesNotReplayInvalidCompletedBatch(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	invalid := schema.AssistantMessage("", []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("same", projectToolReadFile, `{"file_path":"a"}`),
		projectEinoAssistantToolCallForAdmissionTest("same", projectToolReadFile, `{"file_path":"b"}`),
	})
	runState.RecordAssistantReply(projectEinoAssistantReplyFromMessage(invalid))
	config := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{}, runState)
	input := []*schema.Message{schema.UserMessage("fix it")}
	decision := config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt:  1,
		InputMessages: input,
		OutputMessage: invalid,
	})
	if decision.Retry || decision.RewriteError != nil || len(decision.ModifiedInputMessages) != 0 {
		t.Fatalf("decision = %#v, want completed malformed response left to structural admission", decision)
	}
}

func TestProjectEinoAssistantModelRetryConfigRetriesIncompleteResponses(t *testing.T) {
	defaults := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{}, nil)
	if defaults.MaxRetries != 5 || defaults.BackoffFunc(context.Background(), 1) != 200*time.Millisecond {
		t.Fatalf("default retry config = (%d, %s), want (5, 200ms)", defaults.MaxRetries, defaults.BackoffFunc(context.Background(), 1))
	}
	backoffOnly := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{LLM: projectLLMSettings{
		RetryBackoff: 10 * time.Millisecond,
	}}, nil)
	if backoffOnly.MaxRetries != 5 {
		t.Fatalf("backoff-only max retries = %d, want default 5", backoffOnly.MaxRetries)
	}
	disabled := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{LLM: projectLLMSettings{
		MaxRetriesConfigured: true,
		RetryBackoff:         10 * time.Millisecond,
	}}, nil)
	if disabled.MaxRetries != 0 {
		t.Fatalf("explicitly disabled max retries = %d, want 0", disabled.MaxRetries)
	}
	var retryStatuses []string
	config := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{
		LLM: projectLLMSettings{
			MaxRetries:   4,
			RetryBackoff: 10 * time.Millisecond,
		},
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnStatus: func(status string) { retryStatuses = append(retryStatuses, status) },
		},
	}, nil)
	if config.MaxRetries != 4 {
		t.Fatalf("max retries = %d, want provider-configured 4", config.MaxRetries)
	}
	if got := config.BackoffFunc(context.Background(), 3); got != 40*time.Millisecond {
		t.Fatalf("third retry backoff = %s, want 40ms", got)
	}
	timeoutErr := &projectEinoAssistantModelTimeoutError{Code: "model_first_response_timeout"}
	decision := config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 1,
		Err:          timeoutErr,
	})
	if !decision.Retry || decision.RejectReason != "transport failure" {
		t.Fatalf("first timeout decision = %#v, want transport retry", decision)
	}
	if len(retryStatuses) != 1 || retryStatuses[0] != "Model connection was interrupted; reconnecting 1/4" {
		t.Fatalf("pre-stream retry statuses = %#v, want reconnecting 1/4", retryStatuses)
	}
	decision = config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 2,
		Err:          timeoutErr,
	})
	if !decision.Retry || decision.RejectReason != "transport failure" {
		t.Fatalf("second timeout decision = %#v, want provider-configured retry policy", decision)
	}
	decision = config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 1,
		Err:          context.DeadlineExceeded,
	})
	if !decision.Retry {
		t.Fatalf("provider request timeout decision = %#v, want retry", decision)
	}

	decision = config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt:  1,
		OutputMessage: schema.AssistantMessage("partial", nil),
		Err:           &projectEinoAssistantModelTimeoutError{Code: "model_stream_idle_timeout"},
	})
	if !decision.Retry || decision.RejectReason != "transport failure" {
		t.Fatalf("partial stream timeout decision = %#v, want retry before response completion", decision)
	}

	decision = config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt:  1,
		OutputMessage: schema.AssistantMessage("complete", nil),
	})
	if decision.Retry {
		t.Fatalf("completed response decision = %#v, want accepted response", decision)
	}
	decision = config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 5,
		Err:          &projectEinoAssistantIncompleteStreamError{},
	})
	if decision.Retry {
		t.Fatalf("exhausted retry decision = %#v, want original terminal stream error", decision)
	}
}

func TestProjectEinoAssistantReconnectStatusUsesCodexAttemptCounts(t *testing.T) {
	if got := projectEinoAssistantReconnectStatus(1, 5); got != "Model connection was interrupted; reconnecting 1/5" {
		t.Fatalf("first reconnect status = %q", got)
	}
	if got := projectEinoAssistantReconnectStatus(3, 5); got != "Model connection was interrupted; reconnecting 3/5" {
		t.Fatalf("third reconnect status = %q", got)
	}
}

func TestProjectEinoAssistantToolBatchMiddlewarePreservesNativeConcurrentScheduling(t *testing.T) {
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(nil)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var active int32
	var peak int32
	endpoint, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			current := atomic.AddInt32(&active, 1)
			for {
				previous := atomic.LoadInt32(&peak)
				if current <= previous || atomic.CompareAndSwapInt32(&peak, previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			atomic.AddInt32(&active, -1)
			return "ok", nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, callErr := endpoint(context.Background(), `{}`); callErr != nil {
				t.Errorf("read endpoint: %v", callErr)
			}
		}()
	}
	for index := 0; index < 8; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("wrapped tool call serialized Eino's native concurrent batch")
		}
	}
	close(release)
	wait.Wait()
	if got := atomic.LoadInt32(&peak); got != 8 {
		t.Fatalf("peak concurrency = %d, want 8", got)
	}
}

func TestProjectEinoAssistantBoundedModelTimesOutFirstResponseAndIdleStream(t *testing.T) {
	t.Run("first response", func(t *testing.T) {
		model := &projectEinoAssistantBoundedModel{
			BaseChatModel:        projectEinoAssistantTimeoutTestModel{mode: "no-first-response"},
			firstResponseTimeout: 15 * time.Millisecond,
			streamIdleTimeout:    time.Second,
		}
		reader, err := model.Stream(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		_, err = reader.Recv()
		var timeoutErr *projectEinoAssistantModelTimeoutError
		if !errors.As(err, &timeoutErr) || timeoutErr.Code != "model_first_response_timeout" {
			t.Fatalf("Recv error = %v, want first-response timeout", err)
		}
	})

	t.Run("stream idle", func(t *testing.T) {
		model := &projectEinoAssistantBoundedModel{
			BaseChatModel:        projectEinoAssistantTimeoutTestModel{mode: "idle-after-first"},
			firstResponseTimeout: time.Second,
			streamIdleTimeout:    15 * time.Millisecond,
		}
		reader, err := model.Stream(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		message, err := reader.Recv()
		if err != nil || message == nil || message.Content != "started" {
			t.Fatalf("first chunk = %#v, %v", message, err)
		}
		_, err = reader.Recv()
		var timeoutErr *projectEinoAssistantModelTimeoutError
		if !errors.As(err, &timeoutErr) || timeoutErr.Code != "model_stream_idle_timeout" {
			t.Fatalf("second Recv error = %v, want stream-idle timeout", err)
		}
	})
}

func TestProjectEinoAssistantBoundedModelUsesCodexStreamDefaultsAndProviderOverride(t *testing.T) {
	base := projectEinoAssistantTimeoutTestModel{mode: "complete"}
	bounded, ok := projectEinoAssistantBoundModel(base, projectLLMSettings{}).(*projectEinoAssistantBoundedModel)
	if !ok {
		t.Fatalf("bounded model type = %T", bounded)
	}
	if bounded.firstResponseTimeout != 300*time.Second || bounded.streamIdleTimeout != 300*time.Second {
		t.Fatalf("default timeouts = (%s, %s), want 5m", bounded.firstResponseTimeout, bounded.streamIdleTimeout)
	}
	if !bounded.requireCompletion {
		t.Fatal("OpenAI-compatible stream should require a completion marker")
	}

	bounded = projectEinoAssistantBoundModel(base, projectLLMSettings{
		Provider:          projectLLMProviderGoogle,
		StreamIdleTimeout: 17 * time.Second,
	}).(*projectEinoAssistantBoundedModel)
	if bounded.firstResponseTimeout != 17*time.Second || bounded.streamIdleTimeout != 17*time.Second {
		t.Fatalf("provider timeouts = (%s, %s), want 17s", bounded.firstResponseTimeout, bounded.streamIdleTimeout)
	}
	if !bounded.requireCompletion {
		t.Fatal("Google stream should require its native finish reason")
	}
}

func TestProjectEinoAssistantBoundedStreamRequiresTerminalFinishReason(t *testing.T) {
	t.Run("missing marker", func(t *testing.T) {
		model := &projectEinoAssistantBoundedModel{
			BaseChatModel:        projectEinoAssistantTimeoutTestModel{mode: "incomplete"},
			firstResponseTimeout: time.Second,
			streamIdleTimeout:    time.Second,
			requireCompletion:    true,
		}
		reader, err := model.Stream(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		message, err := reader.Recv()
		if err != nil || message == nil || message.Content != "partial" {
			t.Fatalf("first chunk = %#v, %v", message, err)
		}
		_, err = reader.Recv()
		var incomplete *projectEinoAssistantIncompleteStreamError
		if !errors.As(err, &incomplete) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("terminal error = %v, want retryable incomplete stream", err)
		}
	})

	for _, finishReason := range []string{"stop", "tool_calls"} {
		t.Run(finishReason, func(t *testing.T) {
			model := &projectEinoAssistantBoundedModel{
				BaseChatModel:        projectEinoAssistantTimeoutTestModel{mode: finishReason},
				firstResponseTimeout: time.Second,
				streamIdleTimeout:    time.Second,
				requireCompletion:    true,
			}
			reader, err := model.Stream(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := reader.Recv(); err != nil {
				t.Fatalf("completed chunk: %v", err)
			}
			if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
				t.Fatalf("terminal error = %v, want EOF", err)
			}
		})
	}
}

func projectEinoAssistantToolCallForAdmissionTest(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func projectEinoAssistantDistinctReadCallsForAdmissionTest(count int) []schema.ToolCall {
	calls := make([]schema.ToolCall, count)
	for index := range calls {
		calls[index] = projectEinoAssistantToolCallForAdmissionTest(
			fmt.Sprintf("read-%d", index),
			projectToolReadFile,
			fmt.Sprintf(`{"file_path":"src/file-%d.ts"}`, index),
		)
	}
	return calls
}

type projectEinoAssistantTimeoutTestModel struct {
	mode string
}

func (m projectEinoAssistantTimeoutTestModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m projectEinoAssistantTimeoutTestModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		switch m.mode {
		case "idle-after-first":
			writer.Send(schema.AssistantMessage("started", nil), nil)
			<-ctx.Done()
		case "incomplete":
			writer.Send(schema.AssistantMessage("partial", nil), nil)
		case "stop", "tool_calls", "complete":
			message := schema.AssistantMessage("complete", nil)
			message.ResponseMeta = &schema.ResponseMeta{FinishReason: m.mode}
			writer.Send(message, nil)
		default:
			<-ctx.Done()
		}
	}()
	return reader, nil
}
