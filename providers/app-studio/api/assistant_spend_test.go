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
	"io"
	"math"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantOrgMonthlyUSDCapConfiguration(t *testing.T) {
	if projectAssistantDefaultOrgMonthlyUSDCapMicros != 100_000_000 {
		t.Fatalf("default org cap = %d micros, want a finite 100 USD", projectAssistantDefaultOrgMonthlyUSDCapMicros)
	}
	tests := []struct {
		value string
		want  int64
	}{
		{value: "", want: 100_000_000},
		{value: "250", want: 250_000_000},
		{value: "$12.50", want: 12_500_000},
		{value: "0.000001", want: 1},
		// Below half a micro these round to 0, which is the unlimited
		// sentinel. A positive cap must never silently become no cap.
		{value: "0.0000004", want: 1},
		{value: "$0.0000001", want: 1},
		{value: "1e-300", want: 1},
		// Beyond int64 the cap saturates rather than wrapping.
		{value: "1e300", want: math.MaxInt64},
		{value: " unlimited ", want: 0},
		{value: "0", want: 0},
		{value: "-5", want: 100_000_000},
		{value: "invalid", want: 100_000_000},
		{value: "NaN", want: 100_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := projectAssistantOrgMonthlyUSDCapMicrosForValue(tt.value); got != tt.want {
				t.Fatalf("cap for %q = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestProjectAssistantModelCostMicros(t *testing.T) {
	tests := []struct {
		model     string
		in, out   int64
		want      int64
		wantKnown bool
	}{
		{model: "gpt-5.4", in: 1_000_000, out: 1_000_000, want: 17_500_000, wantKnown: true},
		{model: "openai/GPT-5.4-2026-03-01", in: 1_000_000, out: 0, want: 2_500_000, wantKnown: true},
		{model: "gpt-4o-mini-2024-07-18", in: 1_000_000, out: 1_000_000, want: 750_000, wantKnown: true},
		{model: "claude-opus-4-5-20260101", in: 0, out: 1_000_000, want: 25_000_000, wantKnown: true},
		{model: "made-up-model", in: 1_000_000, out: 1_000_000, want: 30_000_000, wantKnown: false},
		{model: "", in: 100, out: 100, want: 3_000, wantKnown: false},
		{model: "gpt-5.4", in: -5, out: -5, want: 0, wantKnown: true},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if _, known := projectAssistantModelPriceFor(tt.model); known != tt.wantKnown {
				t.Fatalf("known(%q) = %t, want %t", tt.model, known, tt.wantKnown)
			}
			if got := projectAssistantModelCostMicros(tt.model, tt.in, tt.out); got != tt.want {
				t.Fatalf("cost(%q, %d, %d) = %d, want %d", tt.model, tt.in, tt.out, got, tt.want)
			}
		})
	}
	// The unknown-model fallback must be a frontier-tier rate: no catalog entry
	// may exceed it on either input or output, except the premium o1* and
	// claude-opus-4* tiers, which are knowingly priced above it. Adding a
	// cheaper-than-fallback entry is fine; adding a pricier one outside those
	// families means the fallback (or this exemption) needs revisiting.
	for id, price := range projectAssistantModelPrices {
		if price.InputPer1M > projectAssistantUnknownModelPrice.InputPer1M || price.OutputPer1M > projectAssistantUnknownModelPrice.OutputPer1M {
			if !strings.HasPrefix(id, "o1") && !strings.HasPrefix(id, "claude-opus-4") {
				t.Errorf("catalog price for %s exceeds the unknown-model fallback", id)
			}
		}
	}
}

// The float to int64 step is where an absurd cost could turn into a credit:
// Go leaves out-of-range conversions implementation-defined and amd64 yields
// MinInt64. The clamp must fail closed at both ends so nothing unpriceable or
// oversized ever lowers an organization's recorded spend.
func TestProjectAssistantClampUSDMicros(t *testing.T) {
	for name, tt := range map[string]struct {
		in   float64
		want int64
	}{
		"nan":          {math.NaN(), math.MaxInt64},
		"+inf":         {math.Inf(1), math.MaxInt64},
		"past int64":   {1e19, math.MaxInt64},
		"at int64":     {float64(math.MaxInt64), math.MaxInt64},
		"-inf":         {math.Inf(-1), 0},
		"negative":     {-3, 0},
		"zero":         {0, 0},
		"rounds down":  {2.4, 2},
		"rounds up":    {2.5, 3},
		"ordinary":     {17_500_000, 17_500_000},
		"half a micro": {0.4, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got := projectAssistantClampUSDMicros(tt.in); got != tt.want {
				t.Fatalf("clamp(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
	// End to end: token counts large enough to overflow int64 at the fallback
	// rate must saturate, never go negative.
	if got := projectAssistantModelCostMicros("made-up-model", 1<<62, 1<<62); got != math.MaxInt64 {
		t.Fatalf("overflowing cost = %d, want MaxInt64", got)
	}
}

func TestProjectEinoAssistantOrgSpendGuardUnderAtOver(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	const capMicros = 10_000_000 // 10 USD
	// gpt-4o: 2.50 in / 10.00 out per 1M → 1M output tokens = 10 USD.
	guard := newProjectEinoAssistantOrgSpendGuard(memory, "org-a", "gpt-4o", capMicros, nil)
	guard.now = func() time.Time { return now }

	// Under: nothing recorded yet.
	if err := guard.Check(ctx); err != nil {
		t.Fatalf("check under cap = %v", err)
	}
	if err := guard.Record(ctx, &schema.TokenUsage{PromptTokens: 0, CompletionTokens: 999_999}); err != nil {
		t.Fatalf("record under cap = %v", err)
	}
	if err := guard.Check(ctx); err != nil {
		t.Fatalf("check just under cap = %v", err)
	}

	// At: exactly the cap blocks the next call.
	if err := guard.Record(ctx, &schema.TokenUsage{CompletionTokens: 1}); err != nil {
		t.Fatalf("record at cap = %v", err)
	}
	err := guard.Check(ctx)
	var capErr *projectAssistantOrgSpendCapExceededError
	if !errors.As(err, &capErr) || !projectEinoAssistantOrgSpendCapExceeded(err) {
		t.Fatalf("check at cap = %v, want org spend cap exceeded", err)
	}
	if capErr.CapMicros != capMicros || capErr.UsedMicros != capMicros {
		t.Fatalf("cap error = %#v, want used == cap == %d", capErr, capMicros)
	}
	if !strings.Contains(err.Error(), "$10.00 of the $10.00 monthly cap") || !strings.Contains(err.Error(), projectAssistantOrgMonthlyUSDCapEnv) {
		t.Fatalf("cap error text = %q, want cap, usage, and the env knob", err.Error())
	}

	// Over: still blocked, and the usage is still counted.
	if err := guard.Record(ctx, &schema.TokenUsage{CompletionTokens: 100_000}); err != nil {
		t.Fatalf("record over cap = %v", err)
	}
	if err := guard.Check(ctx); !projectEinoAssistantOrgSpendCapExceeded(err) {
		t.Fatalf("check over cap = %v, want org spend cap exceeded", err)
	}
	spend, err := memory.GetOrganizationSpend(ctx, "org-a", now)
	if err != nil || spend.USDMicros != 11_000_000 || spend.OutputTokens != 1_100_000 {
		t.Fatalf("recorded spend = %#v, %v", spend, err)
	}

	// The cap is monthly: next month starts clean, and another org is
	// unaffected.
	guard.now = func() time.Time { return now.AddDate(0, 1, 0) }
	if err := guard.Check(ctx); err != nil {
		t.Fatalf("check next month = %v, want under cap", err)
	}
	other := newProjectEinoAssistantOrgSpendGuard(memory, "org-b", "gpt-4o", capMicros, nil)
	other.now = func() time.Time { return now }
	if err := other.Check(ctx); err != nil {
		t.Fatalf("check other org = %v, want under cap", err)
	}

	// Disabled or unscoped guards are nil and therefore inert.
	if g := newProjectEinoAssistantOrgSpendGuard(memory, "org-a", "gpt-4o", 0, nil); g != nil {
		t.Fatal("cap 0 must disable the guard")
	}
	if g := newProjectEinoAssistantOrgSpendGuard(memory, "", "gpt-4o", capMicros, nil); g != nil {
		t.Fatal("missing org must disable the guard")
	}
	if g := newProjectEinoAssistantOrgSpendGuard(nil, "org-a", "gpt-4o", capMicros, nil); g != nil {
		t.Fatal("missing store must disable the guard")
	}
}

func TestProjectEinoAssistantOrgSpendModelStopsRunAndRecordsEvent(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	run := store.AssistantRun{ID: "run-spend", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := memory.SaveAssistantRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	ledger := newProjectAssistantRunEventLedger(memory, scope, run.ID)
	events := 0
	guard := newProjectEinoAssistantOrgSpendGuard(memory, "org-a", "gpt-4o", 10_000_000, func(ctx context.Context, spend store.OrganizationSpend) {
		events++
		if err := ledger.RecordSpendCapReached(ctx, spend); err != nil {
			t.Errorf("RecordSpendCapReached: %v", err)
		}
	})
	base := &projectAssistantRolloutBudgetTestModel{usages: []*schema.TokenUsage{
		{CompletionTokens: 600_000},
		{CompletionTokens: 600_000},
	}}
	model := projectEinoAssistantOrgSpendModelWithGuard(base, guard)

	reader, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("first")})
	if err != nil {
		t.Fatal(err)
	}
	if message, err := reader.Recv(); err != nil || message == nil || message.Content != "response" {
		t.Fatalf("first response = %#v, %v", message, err)
	}
	if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("first terminal = %v, want EOF", err)
	}
	reader.Close()

	// The response that crosses the cap is still delivered: the provider
	// already billed it.
	message, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("second")})
	if err != nil || message == nil || message.Content != "response" {
		t.Fatalf("crossing response = %#v, %v", message, err)
	}
	if events != 1 {
		t.Fatalf("cap reached callbacks = %d, want exactly one", events)
	}

	// Every later sampling boundary fails closed, on both entry points.
	if _, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("third")}); !projectEinoAssistantOrgSpendCapExceeded(err) {
		t.Fatalf("generate after cap = %v, want org spend cap exceeded", err)
	}
	if _, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("fourth")}); !projectEinoAssistantOrgSpendCapExceeded(err) {
		t.Fatalf("stream after cap = %v, want org spend cap exceeded", err)
	}
	if events != 1 || base.calls != 2 {
		t.Fatalf("callbacks = %d, provider calls = %d; want 1 and 2", events, base.calls)
	}

	recorded, err := memory.ListAssistantRunEvents(ctx, scope, run.ID, 0, 10)
	if err != nil || len(recorded) != 1 || recorded[0].Type != projectAssistantRunSpendCapReachedEventType {
		t.Fatalf("run events = %#v, %v; want one spend_cap_reached", recorded, err)
	}
	var payload projectAssistantRunSpendCapReachedPayload
	if err := json.Unmarshal(recorded[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OrgUUID != "org-a" || payload.UsedMicros != 12_000_000 || payload.OutputTokens != 1_200_000 || !strings.Contains(payload.Message, "monthly cap") {
		t.Fatalf("spend cap payload = %#v", payload)
	}
	if payload.CapUSD != projectAssistantFormatUSDMicros(projectAssistantOrgMonthlyUSDCapMicros()) {
		t.Fatalf("spend cap payload cap = %q, want the configured cap", payload.CapUSD)
	}

	// The ledger stays consistent for subsequent tool events after the notice.
	if _, err := ledger.RecordToolRequest(ctx, "call-1", projectAssistantToolSpec{Name: projectToolLS, Risk: projectAssistantToolRiskRead}, map[string]any{"path": "."}); err != nil {
		t.Fatalf("tool request after spend notice: %v", err)
	}
}

func TestProjectEinoAssistantOrgSpendModelForIsInertWithoutStoreOrOrg(t *testing.T) {
	base := &projectAssistantRolloutBudgetTestModel{}
	if got := projectEinoAssistantOrgSpendModelFor(nil, projectAssistantRunRequest{}, base); got != base {
		t.Fatal("nil server must not wrap the model")
	}
	server := &Server{store: store.NewMemoryStore()}
	if got := projectEinoAssistantOrgSpendModelFor(server, projectAssistantRunRequest{}, base); got != base {
		t.Fatal("request without an organization must not wrap the model")
	}
	req := projectAssistantRunRequest{Identity: identity{orgUUID: "org-a"}, LLM: projectLLMSettings{Model: "gpt-4o"}}
	if got := projectEinoAssistantOrgSpendModelFor(server, req, base); got == base {
		t.Fatal("organization-scoped request must wrap the model with the spend guard")
	}
}

func TestProjectAssistantOrgSpendCapFailureShape(t *testing.T) {
	err := &projectAssistantOrgSpendCapExceededError{CapMicros: 100_000_000, UsedMicros: 100_250_000}
	if !projectEinoAssistantBudgetLimited(err) {
		t.Fatal("org spend cap was not classified as budget limited")
	}
	if projectEinoAssistantRolloutBudgetExceeded(err) {
		t.Fatal("org spend cap must not be mistaken for the per-run token budget")
	}
	if got := projectAssistantBudgetLimitedErrorInfo(err); got != "org_spend_cap_exceeded" {
		t.Fatalf("error info = %q", got)
	}
	if got := projectAssistantBudgetLimitedErrorInfo(&projectAssistantSessionBudgetExceededError{}); got != "session_budget_exceeded" {
		t.Fatalf("session error info = %q", got)
	}
	if got := projectAssistantFailureKind(err); got != "org_spend_cap" {
		t.Fatalf("failure kind = %q", got)
	}
	if got := projectAssistantFailureSummary(err, "org_spend_cap"); !strings.Contains(got, "$100.25 of the $100.00 monthly cap") {
		t.Fatalf("failure summary = %q", got)
	}
	if got := projectAssistantAuditReason(err.Error()); got != "org_spend_cap" {
		t.Fatalf("audit reason = %q", got)
	}
	raw := projectAssistantRunErrorJSON(err, projectAssistantBudgetLimitedErrorInfo(err))
	var view projectAssistantRunErrorView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.ErrorInfo != "org_spend_cap_exceeded" || !strings.Contains(view.Message, "monthly cap") || !strings.Contains(view.Message, projectAssistantOrgMonthlyUSDCapEnv) {
		t.Fatalf("terminal run error = %#v", view)
	}
}

// projectAssistantContextAwareSpendStore is a durable store that fails writes
// once the caller's context is done, the way a SQL driver does. The memory
// store ignores its context, so only this fixture can show whether spend
// accounting survives a client that cancels mid-call.
type projectAssistantContextAwareSpendStore struct {
	inner *store.MemoryStore
}

func (s *projectAssistantContextAwareSpendStore) AddOrganizationSpend(
	ctx context.Context,
	orgUUID string,
	at time.Time,
	delta store.OrganizationSpendDelta,
	now time.Time,
) (store.OrganizationSpend, error) {
	if err := ctx.Err(); err != nil {
		return store.OrganizationSpend{}, err
	}
	return s.inner.AddOrganizationSpend(ctx, orgUUID, at, delta, now)
}

func (s *projectAssistantContextAwareSpendStore) GetOrganizationSpend(
	ctx context.Context,
	orgUUID string,
	at time.Time,
) (store.OrganizationSpend, error) {
	if err := ctx.Err(); err != nil {
		return store.OrganizationSpend{}, err
	}
	return s.inner.GetOrganizationSpend(ctx, orgUUID, at)
}

// A client that cancels after the provider has billed a response must not be
// able to keep that response out of the organization ledger. If cancellation
// dropped the write, the monthly cap would be evadable by cancelling every
// request at the right moment.
func TestProjectEinoAssistantOrgSpendRecordsUnderClientCancellation(t *testing.T) {
	memory := store.NewMemoryStore()
	spendStore := &projectAssistantContextAwareSpendStore{inner: memory}
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	guard := newProjectEinoAssistantOrgSpendGuard(spendStore, "org-a", "gpt-4o", 10_000_000, nil)
	guard.now = func() time.Time { return now }

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := guard.Record(canceled, &schema.TokenUsage{CompletionTokens: 400_000}); err != nil {
		t.Fatalf("record with a canceled request context = %v, want the billed usage recorded anyway", err)
	}
	spend, err := memory.GetOrganizationSpend(context.Background(), "org-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if spend.USDMicros != 4_000_000 || spend.OutputTokens != 400_000 {
		t.Fatalf("ledger after a canceled request = %#v, want the billed usage; the cap is evadable by cancelling", spend)
	}

	// The same must hold on the streaming path, whose recording happens in a
	// goroutine after the caller has already gone away.
	streamGuard := newProjectEinoAssistantOrgSpendGuard(spendStore, "org-b", "gpt-4o", 10_000_000, nil)
	streamGuard.now = func() time.Time { return now }
	base := &projectAssistantRolloutBudgetTestModel{usages: []*schema.TokenUsage{{CompletionTokens: 300_000}}}
	model := projectEinoAssistantOrgSpendModelWithGuard(base, streamGuard)

	streamCtx, cancelStream := context.WithCancel(context.Background())
	reader, err := model.Stream(streamCtx, []*schema.Message{schema.UserMessage("first")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatal(err)
	}
	cancelStream()
	for {
		if _, err := reader.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("stream terminal error = %v, want EOF", err)
		}
	}
	reader.Close()

	streamSpend, err := memory.GetOrganizationSpend(context.Background(), "org-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if streamSpend.USDMicros != 3_000_000 || streamSpend.OutputTokens != 300_000 {
		t.Fatalf("stream ledger after cancellation = %#v, want the billed usage", streamSpend)
	}
}

// projectAssistantSpendCancellableStreamModel streams one content chunk with
// no usage and then holds the stream open until the request context is
// cancelled, ending it with the context error — the shape of a client that
// disconnects before the provider's final usage chunk. The provider has
// already billed the prompt by then.
type projectAssistantSpendCancellableStreamModel struct{}

func (projectAssistantSpendCancellableStreamModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return schema.AssistantMessage("response", nil), nil
}

func (projectAssistantSpendCancellableStreamModel) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(schema.AssistantMessage("partial", nil), nil)
		<-ctx.Done()
		writer.Send(nil, ctx.Err())
	}()
	return reader, nil
}

// A stream that ends before its usage chunk (client cancel, provider error, or
// a provider that never reports stream usage) still cost the organization at
// least the prompt. The ledger must never record zero for a billed call.
func TestProjectEinoAssistantOrgSpendRecordsPromptWhenStreamEndsWithoutUsage(t *testing.T) {
	memory := store.NewMemoryStore()
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	guard := newProjectEinoAssistantOrgSpendGuard(memory, "org-a", "gpt-4o", 10_000_000, nil)
	guard.now = func() time.Time { return now }
	model := projectEinoAssistantOrgSpendModelWithGuard(projectAssistantSpendCancellableStreamModel{}, guard)

	prompt := []*schema.Message{schema.UserMessage(strings.Repeat("build me a dashboard ", 200))}
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := model.Stream(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if message, err := reader.Recv(); err != nil || message == nil || message.Content != "partial" {
		t.Fatalf("first chunk = %#v, %v", message, err)
	}
	cancel()
	var terminal error
	for {
		if _, err := reader.Recv(); err != nil {
			terminal = err
			break
		}
	}
	reader.Close()
	if !errors.Is(terminal, context.Canceled) {
		t.Fatalf("stream terminal error = %v, want the cancellation", terminal)
	}

	spend, err := memory.GetOrganizationSpend(context.Background(), "org-a", now)
	if err != nil {
		t.Fatal(err)
	}
	wantInput := int64(projectEinoAssistantMessagesTokenEstimate(prompt))
	if wantInput <= 0 {
		t.Fatalf("test prompt estimates to %d tokens; want a positive estimate", wantInput)
	}
	if spend.InputTokens != wantInput || spend.OutputTokens != 0 {
		t.Fatalf("ledger after a cancelled stream = %#v, want the estimated prompt (%d input tokens) recorded", spend, wantInput)
	}
	if want := projectAssistantModelCostMicros("gpt-4o", wantInput, 0); spend.USDMicros != want || want == 0 {
		t.Fatalf("ledger USD after a cancelled stream = %d, want %d (non-zero)", spend.USDMicros, want)
	}

	// Once the provider does report usage, the report wins over the estimate.
	reported := newProjectEinoAssistantOrgSpendGuard(memory, "org-b", "gpt-4o", 10_000_000, nil)
	reported.now = func() time.Time { return now }
	base := &projectAssistantRolloutBudgetTestModel{usages: []*schema.TokenUsage{{PromptTokens: 7, CompletionTokens: 3}}}
	reader, err = projectEinoAssistantOrgSpendModelWithGuard(base, reported).Stream(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := reader.Recv(); err != nil {
			break
		}
	}
	reader.Close()
	spend, err = memory.GetOrganizationSpend(context.Background(), "org-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if spend.InputTokens != 7 || spend.OutputTokens != 3 {
		t.Fatalf("ledger with reported usage = %#v, want the provider's numbers, not an estimate", spend)
	}
}

// A billed call whose prompt cannot be estimated — empty, or one the estimator
// fails to marshal — must still land in the ledger. A zero there is exactly
// what a caller bypassing the cap would arrange: cancel every stream before its
// usage chunk and hand the provider a prompt the estimator gives up on.
func TestProjectEinoAssistantOrgSpendRecordsFloorForUnestimatablePrompt(t *testing.T) {
	memory := store.NewMemoryStore()
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	guard := newProjectEinoAssistantOrgSpendGuard(memory, "org-a", "gpt-4o", 10_000_000, nil)
	guard.now = func() time.Time { return now }
	model := projectEinoAssistantOrgSpendModelWithGuard(projectAssistantSpendCancellableStreamModel{}, guard)

	var prompt []*schema.Message
	if estimate := projectEinoAssistantMessagesTokenEstimate(prompt); estimate != 0 {
		t.Fatalf("precondition: an empty prompt estimates to %d tokens, want 0", estimate)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := model.Stream(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if message, err := reader.Recv(); err != nil || message == nil || message.Content != "partial" {
		t.Fatalf("first chunk = %#v, %v", message, err)
	}
	cancel()
	for {
		if _, err := reader.Recv(); err != nil {
			break
		}
	}
	reader.Close()

	spend, err := memory.GetOrganizationSpend(context.Background(), "org-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if spend.InputTokens <= 0 || spend.USDMicros <= 0 {
		t.Fatalf("ledger after a cancelled stream with an un-estimatable prompt = %#v, want spend > 0 (the provider billed the call)", spend)
	}

	// The floor holds in micro-USD as well as tokens: a model priced under one
	// micro-USD per input token must not round the floor token back to zero.
	if cost := projectAssistantModelCostMicros("gpt-5-nano", 1, 0); cost != 0 {
		t.Fatalf("precondition: one gpt-5-nano input token prices to %d micro-USD, want 0 (this case is what the micro-USD floor is for)", cost)
	}
	cheap := newProjectEinoAssistantOrgSpendGuard(memory, "org-b", "gpt-5-nano", 10_000_000, nil)
	cheap.now = func() time.Time { return now }
	if err := cheap.RecordAtLeastPrompt(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	spend, err = memory.GetOrganizationSpend(context.Background(), "org-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if spend.InputTokens < 1 || spend.USDMicros < 1 {
		t.Fatalf("ledger for a sub-micro model with an un-estimatable prompt = %#v, want at least one token and one micro-USD", spend)
	}

	// A prompt the estimator can price is still charged at its estimate, not
	// the floor.
	priced := newProjectEinoAssistantOrgSpendGuard(memory, "org-c", "gpt-4o", 10_000_000, nil)
	priced.now = func() time.Time { return now }
	long := []*schema.Message{schema.UserMessage(strings.Repeat("build me a dashboard ", 200))}
	if err := priced.RecordAtLeastPrompt(context.Background(), long, nil); err != nil {
		t.Fatal(err)
	}
	spend, err = memory.GetOrganizationSpend(context.Background(), "org-c", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(projectEinoAssistantMessagesTokenEstimate(long)); spend.InputTokens != want || want <= 1 {
		t.Fatalf("ledger for an estimatable prompt = %#v, want its estimate of %d input tokens", spend, want)
	}
}

// The spend guard is installed for runs that have no event ledger. Reaching
// the cap on such a run must report through the guard without faulting.
func TestProjectEinoAssistantOrgSpendModelForToleratesNilEventLedger(t *testing.T) {
	t.Setenv(projectAssistantOrgMonthlyUSDCapEnv, "0.000001")
	server := &Server{store: store.NewMemoryStore()}
	req := projectAssistantRunRequest{
		Identity: identity{orgUUID: "org-a"},
		LLM:      projectLLMSettings{Model: "gpt-4o"},
	}
	if req.eventLedger != nil {
		t.Fatal("fixture must exercise a run with no event ledger")
	}
	base := &projectAssistantRolloutBudgetTestModel{usages: []*schema.TokenUsage{{CompletionTokens: 1_000}}}
	model := projectEinoAssistantOrgSpendModelFor(server, req, base)
	if model == base {
		t.Fatal("organization-scoped request must wrap the model with the spend guard")
	}

	ctx := context.Background()
	// This call crosses the one-micro cap and therefore reports cap-reached.
	if _, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("first")}); err != nil {
		t.Fatalf("crossing call = %v, want the billed response returned", err)
	}
	// And the next call fails closed rather than calling the provider again.
	if _, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("second")}); !projectEinoAssistantOrgSpendCapExceeded(err) {
		t.Fatalf("call after the cap = %v, want org spend cap exceeded", err)
	}
	if base.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", base.calls)
	}
}

// Caps and usage below one cent must stay legible: an administrator reads the
// configured cap back out of these strings.
func TestProjectAssistantFormatUSDMicros(t *testing.T) {
	tests := []struct {
		micros int64
		want   string
	}{
		{micros: 0, want: "$0.00"},
		{micros: 1, want: "$0.000001"},
		{micros: 2_500, want: "$0.0025"},
		{micros: 10_000, want: "$0.01"},
		{micros: 12_500_000, want: "$12.50"},
		{micros: 100_000_000, want: "$100.00"},
		{micros: 100_250_000, want: "$100.25"},
		{micros: 100_000_001, want: "$100.000001"},
	}
	for _, tt := range tests {
		if got := projectAssistantFormatUSDMicros(tt.micros); got != tt.want {
			t.Errorf("format %d micros = %q, want %q", tt.micros, got, tt.want)
		}
	}
}

// The unconfigured-ledger error is surfaced in logs when a spend cap event
// cannot be written, so it has to name the ledger that is actually missing.
func TestProjectAssistantRunEventLedgerSpendCapNotConfiguredError(t *testing.T) {
	var nilLedger *projectAssistantRunEventLedger
	err := nilLedger.RecordSpendCapReached(context.Background(), store.OrganizationSpend{OrgUUID: "org-a"})
	if err == nil {
		t.Fatal("an unconfigured ledger must report that it cannot record")
	}
	if !strings.Contains(err.Error(), "assistant run event ledger is not configured") {
		t.Fatalf("error = %q, want it to name the run event ledger", err.Error())
	}
	if strings.Contains(err.Error(), "tool ledger") {
		t.Fatalf("error = %q, must not blame the tool ledger for a run event failure", err.Error())
	}
}
