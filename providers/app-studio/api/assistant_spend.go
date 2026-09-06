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
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
)

// Per-organization monthly USD spend cap. Every App Studio model call is
// priced from the provider's reported token usage, added to the
// organization's calendar-month ledger, and the running total is checked
// before each subsequent model call. Iteration and token limits bound one
// run; this cap bounds what an organization can spend across all of its
// projects, runs, and replicas.
const (
	projectAssistantOrgMonthlyUSDCapEnv                 = "APP_STUDIO_ORG_MONTHLY_USD_CAP"
	projectAssistantDefaultOrgMonthlyUSDCapMicros int64 = 100 * projectAssistantUSDMicros
	projectAssistantUnlimitedOrgMonthlyUSDCap     int64 = 0
	projectAssistantUSDMicros                     int64 = 1_000_000
	projectAssistantRunSpendCapReachedEventType         = "spend_cap_reached"
)

var (
	errProjectAssistantOrgSpendCapExceeded        = errors.New("organization monthly spend cap reached")
	projectAssistantOrgSpendCapUnlimitedLogOnce   sync.Once
	projectAssistantOrgSpendCapInvalidValueLogMap sync.Map
)

// projectAssistantOrgSpendCapExceededError is the typed, user-facing failure
// for a run that must stop because its organization has used its monthly
// budget. The message deliberately names the cap and the knob so an
// administrator can act on it without reading provider logs.
type projectAssistantOrgSpendCapExceededError struct {
	CapMicros  int64
	UsedMicros int64
}

func (e *projectAssistantOrgSpendCapExceededError) Error() string {
	return fmt.Sprintf(
		"%s: %s of the %s monthly cap is used; the assistant stops until an administrator raises %s or the next calendar month begins",
		errProjectAssistantOrgSpendCapExceeded.Error(),
		projectAssistantFormatUSDMicros(e.UsedMicros),
		projectAssistantFormatUSDMicros(e.CapMicros),
		projectAssistantOrgMonthlyUSDCapEnv,
	)
}

func (e *projectAssistantOrgSpendCapExceededError) Unwrap() error {
	return errProjectAssistantOrgSpendCapExceeded
}

func projectEinoAssistantOrgSpendCapExceeded(err error) bool {
	return errors.Is(err, errProjectAssistantOrgSpendCapExceeded)
}

// projectAssistantFormatUSDMicros renders micro-USD for humans. Whole cents
// keep the familiar two-decimal form; anything finer keeps its significant
// digits, because a sub-cent cap that printed as "$0.00" would leave an
// administrator unable to read back what the cap actually is. The arithmetic
// is integral so large ledgers do not drift through float64.
func projectAssistantFormatUSDMicros(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	fraction := strconv.FormatInt(projectAssistantUSDMicros+micros%projectAssistantUSDMicros, 10)[1:]
	if trimmed := strings.TrimRight(fraction, "0"); len(trimmed) > 2 {
		fraction = trimmed
	} else {
		fraction = fraction[:2]
	}
	return fmt.Sprintf("$%s%d.%s", sign, micros/projectAssistantUSDMicros, fraction)
}

func projectAssistantOrgMonthlyUSDCapMicros() int64 {
	capMicros := projectAssistantOrgMonthlyUSDCapMicrosForValue(os.Getenv(projectAssistantOrgMonthlyUSDCapEnv))
	if capMicros == projectAssistantUnlimitedOrgMonthlyUSDCap {
		projectAssistantOrgSpendCapUnlimitedLogOnce.Do(func() {
			klog.V(0).Infof("%s disables the organization monthly spend cap; App Studio model spend is unbounded", projectAssistantOrgMonthlyUSDCapEnv)
		})
	}
	return capMicros
}

// projectAssistantOrgMonthlyUSDCapMicrosForValue parses the configured cap in
// USD (decimals allowed) into micro-USD. Empty, negative, or unparsable values
// fall back to the finite default; only an explicit "0" or "unlimited"
// disables the cap.
func projectAssistantOrgMonthlyUSDCapMicrosForValue(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return projectAssistantDefaultOrgMonthlyUSDCapMicros
	}
	if projectAssistantLimitValueUnlimited(value) {
		return projectAssistantUnlimitedOrgMonthlyUSDCap
	}
	usd, err := strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
	if err != nil || math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
		if _, logged := projectAssistantOrgSpendCapInvalidValueLogMap.LoadOrStore(value, struct{}{}); !logged {
			klog.V(0).Infof("ignoring invalid %s=%q; using the default %s", projectAssistantOrgMonthlyUSDCapEnv, value, projectAssistantFormatUSDMicros(projectAssistantDefaultOrgMonthlyUSDCapMicros))
		}
		return projectAssistantDefaultOrgMonthlyUSDCapMicros
	}
	micros := projectAssistantClampUSDMicros(usd * float64(projectAssistantUSDMicros))
	if micros == 0 {
		// A positive value below half a micro-USD rounds to 0, and 0 is the
		// unlimited sentinel. The operator asked for a bound, so give them the
		// smallest one the ledger can express rather than none at all.
		return 1
	}
	return micros
}

// projectAssistantModelPrice is USD per one million tokens. One USD per 1M
// tokens is exactly one micro-USD per token, so the per-1M price doubles as
// the micro-USD-per-token rate.
type projectAssistantModelPrice struct {
	InputPer1M  float64
	OutputPer1M float64
}

// projectAssistantModelPrices is a best-effort 2026 list-price snapshot for
// the model families App Studio is deployed with. Lookup normalizes the id
// (lowercase, provider prefix stripped) and picks the longest matching
// prefix, so "openai/gpt-5.4-2026-03-01" resolves to "gpt-5.4". Prices drift;
// they exist to bound spend, not to bill.
var projectAssistantModelPrices = map[string]projectAssistantModelPrice{
	"gpt-4o":           {InputPer1M: 2.50, OutputPer1M: 10.00},
	"gpt-4o-mini":      {InputPer1M: 0.15, OutputPer1M: 0.60},
	"gpt-4.1":          {InputPer1M: 2.00, OutputPer1M: 8.00},
	"gpt-4.1-mini":     {InputPer1M: 0.40, OutputPer1M: 1.60},
	"gpt-4.1-nano":     {InputPer1M: 0.10, OutputPer1M: 0.40},
	"gpt-5":            {InputPer1M: 1.25, OutputPer1M: 10.00},
	"gpt-5-mini":       {InputPer1M: 0.25, OutputPer1M: 2.00},
	"gpt-5-nano":       {InputPer1M: 0.05, OutputPer1M: 0.40},
	"gpt-5.1":          {InputPer1M: 1.25, OutputPer1M: 10.00},
	"gpt-5.4":          {InputPer1M: 2.50, OutputPer1M: 15.00},
	"gpt-5.4-mini":     {InputPer1M: 0.75, OutputPer1M: 4.50},
	"o1":               {InputPer1M: 15.00, OutputPer1M: 60.00},
	"o3":               {InputPer1M: 2.00, OutputPer1M: 8.00},
	"o3-mini":          {InputPer1M: 1.10, OutputPer1M: 4.40},
	"o4-mini":          {InputPer1M: 1.10, OutputPer1M: 4.40},
	"claude-sonnet-4":  {InputPer1M: 3.00, OutputPer1M: 15.00},
	"claude-opus-4":    {InputPer1M: 15.00, OutputPer1M: 75.00},
	"claude-opus-4-5":  {InputPer1M: 5.00, OutputPer1M: 25.00},
	"claude-haiku-4-5": {InputPer1M: 1.00, OutputPer1M: 5.00},
	"claude-3-5-haiku": {InputPer1M: 0.80, OutputPer1M: 4.00},
	"gemini-2.5-pro":   {InputPer1M: 1.25, OutputPer1M: 10.00},
	"gemini-2.5-flash": {InputPer1M: 0.30, OutputPer1M: 2.50},
	"gemini-3-pro":     {InputPer1M: 2.00, OutputPer1M: 12.00},
	"gemini-3-flash":   {InputPer1M: 0.50, OutputPer1M: 3.00},
}

// projectAssistantUnknownModelPrice is charged for any model id the table
// does not know. It is deliberately a frontier-tier rate: an unknown model
// must err on the side of exhausting the cap early rather than letting an
// unpriced model spend without bound.
var projectAssistantUnknownModelPrice = projectAssistantModelPrice{InputPer1M: 5.00, OutputPer1M: 25.00}

func projectAssistantModelPriceFor(model string) (projectAssistantModelPrice, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	if normalized == "" {
		return projectAssistantUnknownModelPrice, false
	}
	if price, ok := projectAssistantModelPrices[normalized]; ok {
		return price, true
	}
	best := ""
	for id := range projectAssistantModelPrices {
		if strings.HasPrefix(normalized, id) && len(id) > len(best) {
			best = id
		}
	}
	if best != "" {
		return projectAssistantModelPrices[best], true
	}
	return projectAssistantUnknownModelPrice, false
}

// projectAssistantClampUSDMicros turns a float micro-USD amount into the
// int64 the ledger stores, failing closed at both ends. Go leaves a float to
// int conversion of an out-of-range value implementation-defined, and on
// amd64 it yields MinInt64 — a cost that would credit the organization. So:
// NaN and +Inf (an amount that could not be priced) and anything at or past
// the int64 range become MaxInt64, where they count fully against the cap;
// negatives become 0; everything else is rounded to the nearest micro.
func projectAssistantClampUSDMicros(micros float64) int64 {
	switch {
	case math.IsNaN(micros), math.IsInf(micros, 1), micros >= math.MaxInt64:
		return math.MaxInt64
	case micros <= 0:
		return 0
	}
	return int64(math.Round(micros))
}

// projectAssistantModelCostMicros prices one model response. Cached prompt
// tokens are billed at the full input rate: providers discount them
// differently and a conservative estimate is the safe direction for a cap.
func projectAssistantModelCostMicros(model string, inputTokens, outputTokens int64) int64 {
	price, _ := projectAssistantModelPriceFor(model)
	cost := float64(max(inputTokens, 0))*price.InputPer1M + float64(max(outputTokens, 0))*price.OutputPer1M
	return projectAssistantClampUSDMicros(cost)
}

// projectEinoAssistantOrgSpendGuard enforces the organization cap around one
// run's model calls. Check runs before a call and reads the shared ledger so
// concurrent runs in the same organization observe each other's spend; Record
// runs after a response and adds the priced usage.
type projectEinoAssistantOrgSpendGuard struct {
	store        store.OrganizationSpendStore
	orgUUID      string
	model        string
	capMicros    int64
	now          func() time.Time
	onCapReached func(context.Context, store.OrganizationSpend)

	mu          sync.Mutex
	capReported bool
}

func newProjectEinoAssistantOrgSpendGuard(
	spendStore store.OrganizationSpendStore,
	orgUUID, model string,
	capMicros int64,
	onCapReached func(context.Context, store.OrganizationSpend),
) *projectEinoAssistantOrgSpendGuard {
	orgUUID = strings.TrimSpace(orgUUID)
	if spendStore == nil || orgUUID == "" || capMicros <= 0 {
		return nil
	}
	return &projectEinoAssistantOrgSpendGuard{
		store:        spendStore,
		orgUUID:      orgUUID,
		model:        strings.TrimSpace(model),
		capMicros:    capMicros,
		now:          time.Now,
		onCapReached: onCapReached,
	}
}

// Check fails the next model call once the organization's ledger has reached
// the cap.
//
// Enforcement guarantee. Check reads and Record writes; between them the
// provider call happens, so this is deliberately not an atomic reservation and
// the cap is a bound on spend already incurred, not a hard ceiling on spend in
// flight. Concurrent calls — across runs and across replicas — can each pass
// Check while the ledger is still under the cap, so the month can overshoot by
// at most one model call per call in flight at the moment the cap is crossed.
// The overshoot cannot compound: Record adds to the shared row atomically, and
// once the total reaches the cap every subsequent Check fails, so each run
// contributes at most its own single in-flight call. Making this strict would
// mean reserving an estimate of the cost before the provider call and settling
// it afterwards, since the true cost is only known from the response; that
// trade — refusing calls against a worst-case estimate — is not worth it for a
// budget bound whose per-run exposure is already capped by the iteration and
// rollout-token limits.
func (g *projectEinoAssistantOrgSpendGuard) Check(ctx context.Context) error {
	if g == nil {
		return nil
	}
	now := g.now()
	spend, err := g.store.GetOrganizationSpend(ctx, g.orgUUID, now)
	if err != nil {
		return fmt.Errorf("read organization spend: %w", err)
	}
	if spend.USDMicros >= g.capMicros {
		g.reportCapReached(ctx, spend)
		return &projectAssistantOrgSpendCapExceededError{CapMicros: g.capMicros, UsedMicros: spend.USDMicros}
	}
	return nil
}

func (g *projectEinoAssistantOrgSpendGuard) Record(ctx context.Context, usage *schema.TokenUsage) error {
	if g == nil || usage == nil {
		return nil
	}
	inputTokens := int64(max(usage.PromptTokens, 0))
	outputTokens := int64(max(usage.CompletionTokens, 0))
	delta := store.OrganizationSpendDelta{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		USDMicros:    projectAssistantModelCostMicros(g.model, inputTokens, outputTokens),
	}
	now := g.now()
	// The provider has already billed this response, so the ledger write must
	// outlive the client. On the request context a caller could cancel in the
	// window between the provider returning and this write landing, and the
	// usage would never reach the ledger — cancel at that moment on every
	// call and the monthly cap is evaded outright. Detached, like the other
	// durability-critical writes in this package.
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	spend, err := g.store.AddOrganizationSpend(persistCtx, g.orgUUID, now, delta, now)
	if err != nil {
		return fmt.Errorf("record organization spend: %w", err)
	}
	if spend.USDMicros >= g.capMicros {
		g.reportCapReached(persistCtx, spend)
	}
	return nil
}

// reportCapReached appends the run's one durable cap notice. It is detached
// for the same reason the ledger write is: the notice explains why the run
// stopped, and a client that has already gone away is exactly when it is
// needed.
func (g *projectEinoAssistantOrgSpendGuard) reportCapReached(ctx context.Context, spend store.OrganizationSpend) {
	g.mu.Lock()
	reported := g.capReported
	g.capReported = true
	g.mu.Unlock()
	if reported || g.onCapReached == nil {
		return
	}
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	g.onCapReached(persistCtx, spend)
}

// projectEinoAssistantOrgSpendModel wraps every sampling boundary of a run,
// including compaction, with the organization cap. A response that pushes
// the organization over the cap is still returned (the provider has already
// billed it); the next call fails with the typed cap error.
type projectEinoAssistantOrgSpendModel struct {
	einomodel.BaseChatModel
	guard *projectEinoAssistantOrgSpendGuard
}

func projectEinoAssistantOrgSpendModelWithGuard(
	base einomodel.BaseChatModel,
	guard *projectEinoAssistantOrgSpendGuard,
) einomodel.BaseChatModel {
	if base == nil || guard == nil {
		return base
	}
	return &projectEinoAssistantOrgSpendModel{BaseChatModel: base, guard: guard}
}

func (m *projectEinoAssistantOrgSpendModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	if err := m.guard.Check(ctx); err != nil {
		return nil, err
	}
	message, err := m.BaseChatModel.Generate(ctx, input, opts...)
	if err != nil {
		return message, err
	}
	if err := m.guard.Record(ctx, projectEinoAssistantMessageUsage(message)); err != nil {
		return nil, err
	}
	return message, nil
}

func (m *projectEinoAssistantOrgSpendModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.guard.Check(ctx); err != nil {
		return nil, err
	}
	source, err := m.BaseChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer source.Close()
		defer writer.Close()
		var usage *schema.TokenUsage
		for {
			message, receiveErr := source.Recv()
			if errors.Is(receiveErr, io.EOF) {
				if recordErr := m.guard.Record(ctx, usage); recordErr != nil {
					writer.Send(nil, recordErr)
				}
				return
			}
			if receiveErr != nil {
				writer.Send(nil, receiveErr)
				return
			}
			if current := projectEinoAssistantMessageUsage(message); current != nil {
				usage = current
			}
			writer.Send(message, nil)
		}
	}()
	return reader, nil
}

// projectEinoAssistantOrgSpendModel installs the organization cap for a run.
// It is a no-op when the cap is disabled, when the run has no durable store
// (unit fixtures), or when the caller carries no organization.
func projectEinoAssistantOrgSpendModelFor(
	server *Server,
	req projectAssistantRunRequest,
	base einomodel.BaseChatModel,
) einomodel.BaseChatModel {
	if server == nil || server.store == nil {
		return base
	}
	// A run without an event ledger (no durable store for this request) still
	// gets the cap; it just has nowhere to append the notice. Leave the
	// callback unset rather than relying on the ledger's nil receiver, so the
	// guard skips the write instead of manufacturing an error to log.
	var onCapReached func(context.Context, store.OrganizationSpend)
	if ledger := req.eventLedger; ledger != nil {
		onCapReached = func(ctx context.Context, spend store.OrganizationSpend) {
			if err := ledger.RecordSpendCapReached(ctx, spend); err != nil {
				klog.V(1).Infof("record assistant spend cap event for org %s: %v", spend.OrgUUID, err)
			}
		}
	}
	guard := newProjectEinoAssistantOrgSpendGuard(
		server.store,
		req.Identity.orgUUID,
		req.LLM.Model,
		projectAssistantOrgMonthlyUSDCapMicros(),
		onCapReached,
	)
	return projectEinoAssistantOrgSpendModelWithGuard(base, guard)
}

// projectAssistantRunSpendCapReachedPayload is the durable run-event payload
// written once per run when the organization cap is reached.
type projectAssistantRunSpendCapReachedPayload struct {
	OrgUUID      string `json:"orgUUID"`
	PeriodStart  string `json:"periodStart"`
	UsedUSD      string `json:"usedUSD"`
	CapUSD       string `json:"capUSD"`
	UsedMicros   int64  `json:"usedMicros"`
	CapMicros    int64  `json:"capMicros"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	Message      string `json:"message"`
}

func projectAssistantRunSpendCapReachedEventPayload(spend store.OrganizationSpend, capMicros int64) (json.RawMessage, error) {
	err := &projectAssistantOrgSpendCapExceededError{CapMicros: capMicros, UsedMicros: spend.USDMicros}
	return json.Marshal(projectAssistantRunSpendCapReachedPayload{
		OrgUUID:      spend.OrgUUID,
		PeriodStart:  spend.PeriodStart.UTC().Format(time.RFC3339),
		UsedUSD:      projectAssistantFormatUSDMicros(spend.USDMicros),
		CapUSD:       projectAssistantFormatUSDMicros(capMicros),
		UsedMicros:   spend.USDMicros,
		CapMicros:    capMicros,
		InputTokens:  spend.InputTokens,
		OutputTokens: spend.OutputTokens,
		Message:      err.Error(),
	})
}
