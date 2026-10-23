// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

const (
	referenceToolName = "lookup_document"
	referenceCallID   = "lookup-call-1"
	referenceArgs     = `{"document":"report"}`
	referenceText     = "private captured result bytes"
)

type scriptedBackendModel struct {
	responses [][]*schema.Message
	inputs    [][]*schema.Message
	calls     int
}

func (m *scriptedBackendModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return schema.AssistantMessage("unused", nil), nil
}

func (m *scriptedBackendModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *scriptedBackendModel) Stream(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	_ = ctx
	_ = opts
	m.inputs = append(m.inputs, in)
	if m.calls >= len(m.responses) {
		return nil, errors.New("no scripted response")
	}
	response := m.responses[m.calls]
	m.calls++
	return schema.StreamReaderFromArray(response), nil
}

func referenceResponses() [][]*schema.Message {
	return [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:       referenceCallID,
			Function: schema.FunctionCall{Name: referenceToolName, Arguments: referenceArgs},
		}}}},
		{{Role: schema.Assistant, Content: "I found the document passage."}},
	}
}

func referenceOnlyTool(executions, reads *int) Tool {
	payload := json.RawMessage(`{"capture":"capture-17"}`)
	receipt := json.RawMessage(`{"read":"read-1"}`)
	return Tool{
		Name:         referenceToolName,
		ResultPolicy: backend.ToolResultReferenceOnly,
		ExecRich: func(ctx context.Context, args string) (Observation, error) {
			*executions = *executions + 1
			if got := backend.ToolCallIDFromContext(ctx); got != referenceCallID {
				return Observation{}, errors.New("executor did not receive the tool-call identity")
			}
			if args != referenceArgs {
				return Observation{}, errors.New("executor received changed arguments")
			}
			return Observation{Text: referenceText, ResultReference: payload, Receipt: receipt}, nil
		},
		RehydrateResult: func(ctx context.Context, args string, ref backend.ToolResultReference) (Observation, error) {
			*reads = *reads + 1
			if got := backend.ToolCallIDFromContext(ctx); got != referenceCallID {
				return Observation{}, errors.New("reader did not receive the tool-call identity")
			}
			if args != referenceArgs || ref.ToolName != referenceToolName || ref.ToolCallID != referenceCallID {
				return Observation{}, errors.New("reader received a changed binding")
			}
			return Observation{Text: referenceText, ResultReference: payload, Receipt: receipt}, nil
		},
	}
}

func TestReferenceOnlyResultIsCheckpointedAsReferenceAndReopenedOnResume(t *testing.T) {
	model := &scriptedBackendModel{responses: referenceResponses()}
	executions, reads := 0, 0
	tool := referenceOnlyTool(&executions, &reads)
	var checkpoints []Checkpoint
	var events []ToolEvent
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "open report"}}, []Tool{tool}, TurnConfig{MaxIters: 4, CheckpointEvery: 1},
		Callbacks{
			OnCheckpoint: func(ck Checkpoint) { checkpoints = append(checkpoints, ck) },
			OnTool:       func(event ToolEvent) { events = append(events, event) },
		})
	if err != nil {
		t.Fatalf("stream turn: %v", err)
	}
	if executions != 1 || reads != 0 {
		t.Fatalf("initial execution/read counts = %d/%d, want 1/0", executions, reads)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want one after the result", len(checkpoints))
	}
	if len(events) != 1 || events[0].Result != backend.ToolResultReferencePlaceholder || strings.Contains(events[0].Result, referenceText) {
		t.Fatalf("observer event exposed the result: %+v", events)
	}
	if len(model.inputs) != 2 || !hasToolResult(model.inputs[1], referenceText) {
		t.Fatalf("same-turn model did not receive the tool result")
	}
	ck := checkpoints[0]
	var checkpointTool *CheckpointMessage
	for i := range ck.Messages {
		if ck.Messages[i].Role == RoleTool {
			checkpointTool = &ck.Messages[i]
		}
	}
	if checkpointTool == nil || checkpointTool.Content != backend.ToolResultReferencePlaceholder || checkpointTool.ResultReference == nil {
		t.Fatalf("checkpoint did not retain only the bound reference: %#v", checkpointTool)
	}
	encodedCheckpoint, err := json.Marshal(ck)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedCheckpoint), referenceText) {
		t.Fatalf("checkpoint contains tool result bytes: %s", encodedCheckpoint)
	}

	resumeModel := &scriptedBackendModel{responses: [][]*schema.Message{{{Role: schema.Assistant, Content: "Resumed."}}}}
	_, err = New().ResumeTurnWithTools(context.Background(), resumeModel, ck, []Tool{tool}, TurnConfig{MaxIters: 4}, false, "", Callbacks{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if executions != 1 || reads != 1 {
		t.Fatalf("resume re-executed the tool or skipped its reader: execution/read counts = %d/%d", executions, reads)
	}
	if len(resumeModel.inputs) != 1 || !hasToolResult(resumeModel.inputs[0], referenceText) {
		t.Fatalf("resumed model did not receive the reopened result")
	}
}

func TestReferenceOnlyResumeRejectsChangedArgumentsAndMissingReaderBeforeModel(t *testing.T) {
	initialModel := &scriptedBackendModel{responses: referenceResponses()}
	executions, reads := 0, 0
	tool := referenceOnlyTool(&executions, &reads)
	var checkpoint Checkpoint
	_, err := New().StreamTurnWithTools(context.Background(), initialModel,
		[]Message{{Role: RoleUser, Content: "open report"}}, []Tool{tool}, TurnConfig{MaxIters: 4, CheckpointEvery: 1},
		Callbacks{OnCheckpoint: func(ck Checkpoint) { checkpoint = ck }})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tampered := checkpoint
	tampered.Messages = append([]CheckpointMessage(nil), checkpoint.Messages...)
	for i := range tampered.Messages {
		if tampered.Messages[i].Role != RoleAssistant || len(tampered.Messages[i].ToolCalls) == 0 {
			continue
		}
		tampered.Messages[i].ToolCalls = append([]CheckpointToolCall(nil), tampered.Messages[i].ToolCalls...)
		tampered.Messages[i].ToolCalls[0].Args = `{"document":"different"}`
	}
	changedArgsModel := &scriptedBackendModel{responses: [][]*schema.Message{{{Role: schema.Assistant, Content: "should not run"}}}}
	_, err = New().ResumeTurnWithTools(context.Background(), changedArgsModel, tampered, []Tool{tool}, TurnConfig{MaxIters: 4}, false, "", Callbacks{})
	if !errors.Is(err, ErrToolResultReferenceUnavailable) || changedArgsModel.calls != 0 || reads != 0 {
		t.Fatalf("changed binding was not rejected before reader/model: err=%v model calls=%d reads=%d", err, changedArgsModel.calls, reads)
	}

	missingReader := tool
	missingReader.RehydrateResult = nil
	missingReaderModel := &scriptedBackendModel{responses: [][]*schema.Message{{{Role: schema.Assistant, Content: "should not run"}}}}
	_, err = New().ResumeTurnWithTools(context.Background(), missingReaderModel, checkpoint, []Tool{missingReader}, TurnConfig{MaxIters: 4}, false, "", Callbacks{})
	if err == nil || missingReaderModel.calls != 0 {
		t.Fatalf("missing reader did not fail before model execution: err=%v model calls=%d", err, missingReaderModel.calls)
	}
}

func TestReferenceOnlyResumeReportsMissingCurrentCallerAuthorizationSafely(t *testing.T) {
	initialModel := &scriptedBackendModel{responses: referenceResponses()}
	executions, reads := 0, 0
	tool := referenceOnlyTool(&executions, &reads)
	var checkpoint Checkpoint
	_, err := New().StreamTurnWithTools(context.Background(), initialModel,
		[]Message{{Role: RoleUser, Content: "open report"}}, []Tool{tool}, TurnConfig{MaxIters: 4, CheckpointEvery: 1},
		Callbacks{OnCheckpoint: func(ck Checkpoint) { checkpoint = ck }})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	protected := tool
	protected.RehydrateResult = func(context.Context, string, backend.ToolResultReference) (Observation, error) {
		reads++
		return Observation{}, backend.ErrToolResultReferenceCallerAuthorizationUnavailable
	}
	resumeModel := &scriptedBackendModel{responses: [][]*schema.Message{{{Role: schema.Assistant, Content: "should not run"}}}}
	_, err = New().ResumeTurnWithTools(context.Background(), resumeModel, checkpoint, []Tool{protected}, TurnConfig{MaxIters: 4}, false, "", Callbacks{})
	if !errors.Is(err, ErrToolResultReferenceUnavailable) || !strings.Contains(err.Error(), "current caller authorization is unavailable") {
		t.Fatalf("resume error = %v, want safe current-caller authorization reason", err)
	}
	if reads != 1 || resumeModel.calls != 0 {
		t.Fatalf("reader/model calls = %d/%d, want reader once and no model call", reads, resumeModel.calls)
	}
}

func TestReferenceOnlyCompactionRedactsBytesAndPreservesReference(t *testing.T) {
	ref, err := BindToolResultReference(referenceToolName, referenceCallID, referenceArgs,
		json.RawMessage(`{"capture":"capture-17"}`), json.RawMessage(`{"read":"read-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{
		{Role: RoleUser, Content: strings.Repeat("older history ", 500)},
		{Role: RoleUser, Content: "open report"},
		{Role: RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID:       referenceCallID,
			Function: schema.FunctionCall{Name: referenceToolName, Arguments: referenceArgs},
		}}},
		{Role: RoleTool, Name: referenceToolName, ToolCallID: referenceCallID, Content: referenceText, ResultReference: &ref},
	}
	wire, identities := toEinoWithIdentities(history)
	task := latestUserMessage(history)
	compactorSawPlaceholder := false
	cfg := TurnConfig{ContextBudgetTokens: 200, ContextCompactor: func(_ context.Context, compactHistory []Message, _ ContextEstimate) ([]Message, error) {
		for _, message := range compactHistory {
			if message.Role != RoleTool {
				continue
			}
			compactorSawPlaceholder = message.Content == backend.ToolResultReferencePlaceholder && !strings.Contains(message.Content, referenceText)
		}
		return append([]Message(nil), compactHistory[1:]...), nil
	}}
	rebuilt, rebuiltIdentities, compacted, err := compactBeforeRequest(context.Background(), wire, identities, 0, task, cfg)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !compacted || !compactorSawPlaceholder {
		t.Fatalf("compaction=%v compactor saw placeholder=%v", compacted, compactorSawPlaceholder)
	}
	var toolMessage *schema.Message
	for _, message := range rebuilt {
		if message.Role == schema.Tool {
			toolMessage = message
		}
	}
	if toolMessage == nil || toolMessage.Content != backend.ToolResultReferencePlaceholder || rebuiltIdentities[toolMessage].resultReference == nil {
		t.Fatalf("compaction lost or exposed the result reference: %+v", toolMessage)
	}

	tool := referenceOnlyTool(new(int), new(int))
	if err := rehydrateReferenceOnly(context.Background(), rebuilt, rebuiltIdentities, map[string]Tool{tool.Name: tool}, nil); err != nil {
		t.Fatalf("rehydrate compacted history: %v", err)
	}
	if toolMessage.Content != referenceText {
		t.Fatalf("compacted result was not reopened: %q", toolMessage.Content)
	}

	badCfg := cfg
	badCfg.ContextCompactor = func(_ context.Context, compactHistory []Message, _ ContextEstimate) ([]Message, error) {
		out := append([]Message(nil), compactHistory[1:]...)
		for i := range out {
			if out[i].Role == RoleTool {
				out[i].Content = referenceText
			}
		}
		return out, nil
	}
	_, _, _, err = compactBeforeRequest(context.Background(), wire, identities, 0, task, badCfg)
	if !errors.Is(err, ErrToolResultReferenceUnavailable) {
		t.Fatalf("compactor replacement containing result bytes was accepted: %v", err)
	}
}

func TestToolEventCarriesValidatedSourceBackedPresentation(t *testing.T) {
	const html = "<html>trusted view</html>"
	digest := sha256.Sum256([]byte(html))
	source := store.SourceReference{
		Label:  "Document",
		Object: store.SourceObject{Version: "v1", Resource: "documents", Namespace: "team", Name: "report", UID: "uid-1"},
		Access: []store.SourceAccess{{Version: "v1", Resource: "documents", Namespace: "team", Name: "report", Verb: "get"}},
	}
	presentation := &store.ProviderPresentation{
		Version: 1,
		Title:   "Document view",
		MCPApp: &store.MCPAppResource{
			ResourceURI: "ui://provider.example/document-view",
			MIMEType:    store.MCPAppMIMEType,
			HTML:        html,
			SHA256:      hex.EncodeToString(digest[:]),
		},
	}
	toolModel := &scriptedBackendModel{responses: referenceResponses()}
	toolModel.responses[0][0].ToolCalls[0].Function.Name = referenceToolName
	var event ToolEvent
	_, err := New().StreamTurnWithTools(context.Background(), toolModel,
		[]Message{{Role: RoleUser, Content: "open report"}}, []Tool{{
			Name: referenceToolName,
			ExecRich: func(context.Context, string) (Observation, error) {
				return Observation{Text: "Report contents", Sources: []store.SourceReference{source}, Presentation: presentation}, nil
			},
		}}, TurnConfig{MaxIters: 4}, Callbacks{OnTool: func(got ToolEvent) { event = got }})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !event.HasMCPApp() || len(event.Sources) != 1 || event.Presentation == nil || event.Presentation.MCPApp.HTML != html {
		t.Fatalf("tool event lost validated provenance or app: %+v", event)
	}
	source.Access[0].Verb = "delete"
	presentation.MCPApp.HTML = "mutated"
	if event.Sources[0].Access[0].Verb != "get" || event.Presentation.MCPApp.HTML != html {
		t.Fatalf("tool event metadata aliases adapter memory: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), html) {
		t.Fatalf("serialized event contains embedded app HTML: %s", encoded)
	}
}

func hasToolResult(messages []*schema.Message, content string) bool {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content == content {
			return true
		}
	}
	return false
}
