// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package llm resolves a tenant's model credentials and builds Eino chat
// models.
//
// A credential is a ModelCredential object in the tenant workspace plus the
// Secret it points at: the object carries the provider flavour, the base URL
// and the default model id, and spec.secretRef names the Secret holding the
// API key. Agents map run purposes (chat, background, compaction) to
// ModelCredential names.
//
// The name→Secret convention this replaced (railgrid-agents-model-<name>, with
// the endpoint configuration stuffed into the Secret's own keys) is gone. It
// could not be validated — nothing watched a Secret, so nothing could say
// whether the endpoint answered — and it gave the portal nothing to address a
// probe at before the workspace had its first agent.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/anthropicmodel"
	"github.com/railgrid/provider-sdk/modelcatalog"
)

// SecretGetter is the minimal tenant-Secret read surface this package needs.
type SecretGetter interface {
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
}

// CredentialGetter reads one ModelCredential from the tenant workspace.
type CredentialGetter interface {
	GetModelCredential(ctx context.Context, name string) (*agentsv1alpha1.ModelCredential, error)
}

// CredentialResolver is everything LoadCredential needs: the object and the
// Secret it points at, both read with the same identity. The agents client
// satisfies it as the caller; the virtual-workspace adapter in api/ satisfies
// it as the provider, for unattended runs that have no caller to borrow.
type CredentialResolver interface {
	SecretGetter
	CredentialGetter
}

const (
	// SecretNamespace is the namespace a workspace's agents Secrets live in —
	// model credentials and per-connection credentials alike.
	SecretNamespace = "default"

	// DefaultProfile is used when an agent does not map a purpose to a
	// credential.
	DefaultProfile = "chat"

	// Run purposes an agent may map to a model credential in spec.models.
	// PurposeChat is the interactive/strong model and the fallback for every
	// other purpose; PurposeBackground is the cheap model used for scoped
	// sub-agent workers; PurposeCompaction summarizes long sessions.
	PurposeChat       = "chat"
	PurposeBackground = "background"
	PurposeCompaction = "compaction"

	ProviderOpenAICompatible = agentsv1alpha1.ModelProviderOpenAICompatible
	ProviderOpenAI           = agentsv1alpha1.ModelProviderOpenAI
	ProviderGoogle           = "google"

	// The harness identities. They are named here so this package can REFUSE
	// them by name (see BuildModel); nothing in llm ever uses one.
	ProviderClaudeCode = agentsv1alpha1.ModelProviderClaudeCode
	ProviderCodex      = agentsv1alpha1.ModelProviderCodex

	// DefaultBaseURL is what an empty spec.baseURL falls back to. The CRD
	// requires one, so this only covers an object written before the schema
	// did.
	DefaultBaseURL = "https://api.openai.com/v1"
)

// ErrNotConfigured means the credential resolved to nothing usable.
var ErrNotConfigured = errors.New("no model credentials configured — create a ModelCredential for this workspace")

// ErrCredentialNotFound means the named ModelCredential does not exist.
var ErrCredentialNotFound = errors.New("model credential not found")

// Profile is one resolved credential: the endpoint plus the key behind it.
// It never leaves the process.
type Profile struct {
	ReasoningEffort string `json:"-"`
	// ResponsesOriginKey authenticates private continuation provenance. Durable
	// callers supply a provider-held key shared across replicas; empty uses an
	// ephemeral process key for probes and other non-durable callers.
	ResponsesOriginKey []byte `json:"-"`
	// Transport pins the protocol of an admitted run. Empty selects it from
	// the endpoint, model and effort.
	Transport            string `json:"-"`
	Provider             string `json:"provider,omitempty"`
	BaseURL              string `json:"baseURL,omitempty"`
	Model                string `json:"model,omitempty"`
	APIKey               string `json:"apiKey,omitempty"`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`
}

func (p Profile) normalized() Profile {
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Provider == "" {
		p.Provider = ProviderOpenAICompatible
	}
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	if p.BaseURL == "" {
		p.BaseURL = DefaultBaseURL
	}
	p.Model = strings.TrimSpace(p.Model)
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.AnthropicWorkspaceID = strings.TrimSpace(p.AnthropicWorkspaceID)
	return p
}

// Normalized exposes the defaulting the package applies before a profile is
// used, for callers that build one themselves (the probe verbs).
func (p Profile) Normalized() Profile { return p.normalized() }

// SecretKey returns the Secret key a credential reads its API key from.
func SecretKey(spec agentsv1alpha1.ModelCredentialSpec) string {
	if k := strings.TrimSpace(spec.SecretKey); k != "" {
		return k
	}
	return agentsv1alpha1.DefaultModelSecretKey
}

// APIKeyFromSecret reads the credential's key out of its Secret. An empty
// result means the key is absent, which is a condition the caller reports
// rather than an error.
func APIKeyFromSecret(sec *corev1.Secret, spec agentsv1alpha1.ModelCredentialSpec) string {
	if sec == nil {
		return ""
	}
	key := SecretKey(spec)
	if v, ok := sec.Data[key]; ok {
		return strings.TrimSpace(string(v))
	}
	if v, ok := sec.StringData[key]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// ProfileFor builds the profile for an already-read ModelCredential, reading
// its Secret through c.
func ProfileFor(ctx context.Context, c SecretGetter, cred *agentsv1alpha1.ModelCredential) (Profile, error) {
	if cred.Spec.ConnectionRef != "" {
		getter, ok := c.(CredentialGetter)
		if !ok {
			return Profile{}, fmt.Errorf("provider connection cannot be resolved: %w", ErrNotConfigured)
		}
		connection, err := getter.GetModelCredential(ctx, cred.Spec.ConnectionRef)
		if err != nil {
			return Profile{}, err
		}
		resolved, err := agentsv1alpha1.ResolveModelConnection(cred, connection)
		if err != nil {
			return Profile{}, err
		}
		cred = resolved
	}
	sec, err := c.GetSecret(ctx, SecretNamespace, strings.TrimSpace(cred.Spec.SecretRef.Name))
	if err != nil {
		return Profile{}, err
	}
	key := APIKeyFromSecret(sec, cred.Spec)
	if key == "" {
		return Profile{}, fmt.Errorf("secret %q has no %q key: %w", cred.Spec.SecretRef.Name, SecretKey(cred.Spec), ErrNotConfigured)
	}
	return Profile{
		Provider:             cred.Spec.Provider,
		BaseURL:              cred.Spec.BaseURL,
		Model:                cred.Spec.Model,
		APIKey:               key,
		AnthropicWorkspaceID: cred.Spec.AnthropicWorkspaceID,
	}.normalized(), nil
}

// LoadCredential resolves a ModelCredential name to a usable profile:
// name → ModelCredential → Secret. Both reads use the identity behind c.
func LoadCredential(ctx context.Context, c CredentialResolver, name string) (Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Profile{}, ErrNotConfigured
	}
	cred, err := c.GetModelCredential(ctx, name)
	if err != nil {
		return Profile{}, err
	}
	return ProfileFor(ctx, c, cred)
}

// ErrHarnessCredential means a harness identity was handed to something that
// wanted a chat model. Callers match on it rather than on the message.
var ErrHarnessCredential = errors.New("this model credential is a coding-harness identity, not a chat endpoint")

// BuildModel constructs an Eino chat model from a profile, using native Eino
// Responses support when pinned or selected by endpoint-specific routing, and
// Chat Completions otherwise.
//
// A harness identity is REFUSED before anything else, including the empty-key
// and empty-model checks, because it is the one failure whose cause is not
// visible downstream: a `claude setup-token` value posted to
// /chat/completions comes back as an upstream 401, and the reader is then
// looking for a rotated key instead of a mis-pointed agent.
func BuildModel(ctx context.Context, p Profile) (einomodel.BaseChatModel, error) {
	p = p.normalized()
	if agentsv1alpha1.IsHarnessProvider(p.Provider) {
		return nil, fmt.Errorf("%w: provider %q runs on an edge harness, so point the agent at it with spec.backend.harness.credentialRef instead of spec.backend.model.credentials", ErrHarnessCredential, p.Provider)
	}
	if p.APIKey == "" {
		return nil, ErrNotConfigured
	}
	if p.Model == "" {
		return nil, fmt.Errorf("model credential has no model id")
	}
	switch p.Provider {
	case ProviderOpenAICompatible, ProviderOpenAI, "":
		if err := ValidateReasoningEffort(p.Provider, p.BaseURL, p.Model, p.ReasoningEffort); err != nil {
			return nil, err
		}
		switch ModelTransport(p) {
		case TransportAnthropicMessages:
			return anthropicmodel.New(ctx, anthropicmodel.Config{
				BaseURL: p.BaseURL, Model: p.Model, APIKey: p.APIKey,
				WorkspaceID: p.AnthropicWorkspaceID, Effort: p.ReasoningEffort,
			})
		case TransportResponses:
			return NewResponsesModel(ctx, p)
		case TransportChatCompletions:
		default:
			return nil, fmt.Errorf("unsupported model transport %q", p.Transport)
		}
		cfg := &openaimodel.ChatModelConfig{
			APIKey:          p.APIKey,
			BaseURL:         strings.TrimRight(p.BaseURL, "/"),
			Model:           p.Model,
			HTTPClient:      &http.Client{},
			ReasoningEffort: openaimodel.ReasoningEffortLevel(EffectiveReasoningEffort(p)),
		}
		if p.AnthropicWorkspaceID != "" {
			cfg.HTTPClient.Transport = anthropicWorkspaceTransport{workspaceID: p.AnthropicWorkspaceID, base: http.DefaultTransport}
		}
		// Anthropic's newer models reject an explicit temperature. Let the
		// compatibility endpoint select its model default on every request.
		if modelSupportsTemperature(p.Model) && !isAnthropicEndpoint(p.BaseURL) {
			t := float32(0.2)
			cfg.Temperature = &t
		}
		m, err := openaimodel.NewChatModel(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("create OpenAI-compatible chat model: %w", err)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("provider %q is not supported yet (use %q)", p.Provider, ProviderOpenAICompatible)
	}
}

const (
	TransportChatCompletions   = "chat-completions"
	TransportResponses         = "responses"
	TransportAnthropicMessages = "anthropic-messages"
)

// ModelTransport selects the wire protocol for a model connection. A persisted
// protocol takes precedence when resuming an admitted run. The official
// Anthropic endpoint uses native Messages. Automatic GPT-6
// routing to Responses applies only to the official OpenAI endpoint; custom
// OpenAI-compatible gateways keep Chat Completions unless a caller pinned
// Responses explicitly. Earlier models keep Chat Completions unless their
// selected effort requires Responses on the official endpoint.
func ModelTransport(p Profile) string {
	if transport := strings.TrimSpace(p.Transport); transport != "" {
		return transport
	}
	p = p.normalized()
	if p.Provider != ProviderOpenAICompatible && p.Provider != ProviderOpenAI {
		return TransportChatCompletions
	}
	provider := p.Provider
	if provider == ProviderOpenAI {
		provider = ProviderOpenAICompatible
	}
	if anthropicmodel.UsesEndpoint(p.BaseURL) {
		return TransportAnthropicMessages
	}
	if !modelcatalog.IsOfficialOpenAIEndpoint(provider, p.BaseURL) {
		return TransportChatCompletions
	}
	model := normalizeModelID(p.Model)
	for _, prefix := range []string{"gpt-6", "gpt6"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") || strings.HasPrefix(model, prefix+".") {
			return TransportResponses
		}
	}
	if profile, ok := modelcatalog.ReasoningProfileForConnection(ProviderOpenAICompatible, p.BaseURL, p.Model); ok &&
		profile.ResponsesForReasoning && p.ReasoningEffort != "" && p.ReasoningEffort != "none" {
		return TransportResponses
	}
	return TransportChatCompletions
}

// EffectiveReasoningEffort includes Chat Completions compatibility defaults,
// such as Luna's implicit "none" on compatible gateways. Responses leaves an
// omitted effort to the model's own default.
func EffectiveReasoningEffort(p Profile) string {
	if p.ReasoningEffort != "" {
		return p.ReasoningEffort
	}
	if ModelTransport(p) == TransportChatCompletions {
		return string(modelReasoningEffort(p.Model))
	}
	return ""
}

// ---- discovery --------------------------------------------------------------

// DiscoverModels calls GET {baseURL}/models and returns the chat-capable model
// ids served by the agent's supported model transports plus the round-trip
// latency. A non-2xx status or transport
// error is returned as err, with the latency still measured for the health
// badge.
//
// It lives here rather than in api/ because two callers need exactly the same
// call with exactly the same bounds: the `discover` verb, which answers a
// person waiting on a form, and the ModelCredential reconciler, which decides
// the Reachable condition. A second implementation would be a second answer.
//
// The curation (FilterChatModels, see chatmodels.go) is applied HERE for the
// same reason. The endpoint answers with everything the account can reach —
// speech, embeddings, images, and model families outside this agent's chat
// transports — so an uncurated list is a list of ways to save a credential that
// fails on its first turn. Filtering at the single shared call keeps
// status.models and the verb's answer identical.
func DiscoverModels(ctx context.Context, profile Profile) ([]string, time.Duration, error) {
	profile = profile.normalized()
	base := strings.TrimRight(profile.BaseURL, "/")
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	client := *http.DefaultClient
	if profile.AnthropicWorkspaceID != "" {
		baseTransport := client.Transport
		if baseTransport == nil {
			baseTransport = http.DefaultTransport
		}
		client.Transport = anthropicWorkspaceTransport{workspaceID: profile.AnthropicWorkspaceID, base: baseTransport}
	}
	if usesOfficialAnthropicModelsEndpoint(profile) {
		// The Models API is paginated, and a redirect must not move this
		// credential-bearing request to a different host. Each page is built from
		// the configured official endpoint and its documented after_id cursor.
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
		started := time.Now()
		ids, err := discoverAnthropicModelIDs(ctx, &client, profile, base)
		latency := time.Since(started)
		if err != nil {
			return nil, latency, err
		}
		return FilterChatModels(ids), latency, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, 0, err
	}
	if profile.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+profile.APIKey)
	}
	// Anthropic's OpenAI-compatible chat endpoint shares the native Models
	// API, which requires a version even though chat completions does not.
	if strings.EqualFold(req.URL.Hostname(), "api.anthropic.com") {
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return nil, latency, err
	}
	defer func() { _ = resp.Body.Close() }()
	// 8MB cap: aggregator /models responses are huge (OpenRouter ships several
	// MB of per-model metadata) and a truncated body fails to parse, which used
	// to report "healthy, zero served models".
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, latency, &ProbeError{Status: resp.StatusCode, Msg: msg}
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 2xx but unexpected body — the endpoint is reachable, just not a
		// standard /models list. Treat as healthy with no discovered models.
		return nil, latency, nil
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return FilterChatModels(ids), latency, nil
}

// The official Anthropic Models API accepts up to 1,000 models per page. Keep
// both pagination work and accumulated response data bounded; discovery fails
// rather than reporting a partial list when an endpoint exceeds either bound.
const (
	anthropicModelsPageLimit = 1000
	maxAnthropicModelPages   = 10
	maxAnthropicModelCount   = 5000
	maxModelListResponseSize = 8 << 20
	maxAnthropicModelIDSize  = 1024
)

type anthropicModelListPage struct {
	Data *[]struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore *bool   `json:"has_more"`
	LastID  *string `json:"last_id"`
}

func usesOfficialAnthropicModelsEndpoint(profile Profile) bool {
	if !strings.EqualFold(profile.Provider, ProviderOpenAICompatible) && !strings.EqualFold(profile.Provider, ProviderOpenAI) {
		return false
	}
	return anthropicmodel.UsesEndpoint(profile.BaseURL)
}

func discoverAnthropicModelIDs(ctx context.Context, client *http.Client, profile Profile, base string) ([]string, error) {
	ids := make([]string, 0)
	seenIDs := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	afterID := ""

	for pageNumber := 0; pageNumber < maxAnthropicModelPages; pageNumber++ {
		pageURL, err := anthropicModelsPageURL(base, afterID)
		if err != nil {
			return nil, fmt.Errorf("build Anthropic models page URL: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		if profile.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+profile.APIKey)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxModelListResponseSize+1))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := strings.TrimSpace(string(body))
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, &ProbeError{Status: resp.StatusCode, Msg: msg}
		}
		if readErr != nil {
			return nil, fmt.Errorf("read Anthropic models page: %w", readErr)
		}
		if len(body) > maxModelListResponseSize {
			return nil, fmt.Errorf("anthropic models page exceeds %d-byte limit", maxModelListResponseSize)
		}

		var page anthropicModelListPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode Anthropic models page: %w", err)
		}
		if page.Data == nil || page.HasMore == nil {
			return nil, fmt.Errorf("anthropic models page is missing data or has_more")
		}
		if afterID != "" && len(*page.Data) == 0 {
			return nil, fmt.Errorf("anthropic models page after cursor contains no model data")
		}
		if len(*page.Data) > anthropicModelsPageLimit {
			return nil, fmt.Errorf("anthropic models page exceeds %d-model limit", anthropicModelsPageLimit)
		}
		if len(ids)+len(*page.Data) > maxAnthropicModelCount {
			return nil, fmt.Errorf("anthropic models result exceeds %d-model limit", maxAnthropicModelCount)
		}
		if *page.HasMore {
			if len(*page.Data) == 0 {
				return nil, fmt.Errorf("anthropic models page has_more is true with no model data")
			}
			if page.LastID == nil || strings.TrimSpace(*page.LastID) == "" {
				return nil, fmt.Errorf("anthropic models page has_more is true without last_id")
			}
			cursor := strings.TrimSpace(*page.LastID)
			if len(cursor) > maxAnthropicModelIDSize {
				return nil, fmt.Errorf("anthropic models page contains an invalid last_id cursor")
			}
			if _, repeated := seenCursors[cursor]; repeated || cursor == afterID {
				return nil, fmt.Errorf("anthropic models pagination repeated cursor %q", cursor)
			}
		}

		pageIDs := make([]string, 0, len(*page.Data))
		for _, model := range *page.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" || len(id) > maxAnthropicModelIDSize {
				return nil, fmt.Errorf("anthropic models page contains an invalid model ID")
			}
			if _, duplicate := seenIDs[id]; duplicate {
				return nil, fmt.Errorf("anthropic models page repeats model ID %q", id)
			}
			seenIDs[id] = struct{}{}
			pageIDs = append(pageIDs, id)
			ids = append(ids, id)
		}

		if len(pageIDs) > 0 {
			if page.LastID == nil || strings.TrimSpace(*page.LastID) == "" || strings.TrimSpace(*page.LastID) != pageIDs[len(pageIDs)-1] {
				return nil, fmt.Errorf("anthropic models page has an invalid last_id cursor")
			}
		}

		if !*page.HasMore {
			return ids, nil
		}
		cursor := strings.TrimSpace(*page.LastID)
		seenCursors[cursor] = struct{}{}
		afterID = cursor
	}

	return nil, fmt.Errorf("anthropic models pagination exceeded %d pages", maxAnthropicModelPages)
}

func anthropicModelsPageURL(base, afterID string) (string, error) {
	u, err := url.Parse(base + "/models")
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("limit", strconv.Itoa(anthropicModelsPageLimit))
	if afterID != "" {
		query.Set("after_id", afterID)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// The same client handles verification, streaming and ordinary agent runs.
// Restrict the workspace header to Anthropic, including on redirects.
type anthropicWorkspaceTransport struct {
	workspaceID string
	base        http.RoundTripper
}

func isAnthropicEndpoint(baseURL string) bool {
	endpoint, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(endpoint.Hostname(), "api.anthropic.com")
}

func (t anthropicWorkspaceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.EqualFold(req.URL.Hostname(), "api.anthropic.com") {
		req = req.Clone(req.Context())
		req.Header.Set("anthropic-workspace-id", t.workspaceID)
	}
	return t.base.RoundTrip(req)
}

// ProbeTimeout bounds one discovery call. A person is waiting on the verb and
// a reconciler is holding a work queue slot, so neither may hang on a dead
// endpoint.
const ProbeTimeout = 12 * time.Second

// MaxStatusModels bounds what the reconciler writes to
// ModelCredential.status.models, matching the kind's MaxItems.
const MaxStatusModels = 500

// ProbeError is a non-2xx answer from a model endpoint.
type ProbeError struct {
	Status int
	Msg    string
}

func (e *ProbeError) Error() string {
	if e.Msg != "" {
		return "endpoint returned HTTP " + strconv.Itoa(e.Status) + ": " + e.Msg
	}
	return e.Summary()
}

// Summary is the error WITHOUT the upstream body: the status code and nothing
// else.
//
// It exists because an upstream body is not ours to keep. Providers quote the
// offending request back — OpenAI redacts the key it rejected, but nothing
// makes that universal, and a gateway that echoes the Authorization header
// would put a live key in whatever we wrote the body to. That is tolerable in
// a response a caller already holds the key for, and not tolerable in
// ModelCredential.status, which is a durable object anyone with read access
// lists. So status keeps the code, and the body stays in the answer to the
// person who asked for it.
func (e *ProbeError) Summary() string {
	return "endpoint returned HTTP " + strconv.Itoa(e.Status)
}

// ProbeSummary renders a probe failure for anything durable: the HTTP status
// for a refused call, the transport error otherwise (which names the host, not
// the credential).
func ProbeSummary(err error) string {
	if err == nil {
		return ""
	}
	var pe *ProbeError
	if errors.As(err, &pe) {
		return pe.Summary()
	}
	return err.Error()
}

// modelReasoningEffort returns the Chat Completions reasoning mode required by
// a model. Luna defaults to reasoning in OpenAI-compatible gateways, where
// function tools are rejected unless reasoning is explicitly disabled. Keep
// the field absent for every other model so the endpoint retains its default.
func modelReasoningEffort(model string) openaimodel.ReasoningEffortLevel {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	if m == "gpt-5.6-luna" || m == "gpt-6-luna" {
		return openaimodel.ReasoningEffortLevel("none")
	}
	return ""
}

// modelSupportsTemperature reports whether the model accepts a custom sampling
// temperature. OpenAI's GPT-5/GPT-6 families and o-series reasoning models fix it.
func modelSupportsTemperature(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "gpt5"),
		strings.HasPrefix(m, "gpt-6"), strings.HasPrefix(m, "gpt6"):
		return false
	case strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return false
	}
	return true
}

// ReasoningEfforts exposes only efforts supported by the Agents transport.
func ReasoningEfforts(provider, baseURL, model string) []string {
	if provider == ProviderOpenAI {
		provider = ProviderOpenAICompatible
	}
	profile, ok := modelcatalog.ReasoningProfileForConnection(provider, baseURL, model)
	if !ok {
		return []string{}
	}
	return profile.Efforts
}

// DefaultReasoningEffortForConnection returns the documented default only
// when the exact model is supported by the configured official endpoint. For
// other connections, preserve the existing OpenAI-compatible model defaults.
func DefaultReasoningEffortForConnection(provider, baseURL, model string) string {
	if provider == ProviderOpenAI {
		provider = ProviderOpenAICompatible
	}
	if profile, ok := modelcatalog.ReasoningProfileForConnection(provider, baseURL, model); ok && profile.AnthropicMessages {
		return profile.DefaultEffort
	}
	return DefaultReasoningEffort(model)
}

func ValidateReasoningEffort(provider, baseURL, model, effort string) error {
	if effort == "" {
		return nil
	}
	for _, supported := range ReasoningEfforts(provider, baseURL, model) {
		if effort == supported {
			return nil
		}
	}
	return fmt.Errorf("reasoning effort %q is not supported by this model connection", effort)
}

func DefaultReasoningEffort(model string) string {
	return EffectiveReasoningEffort(Profile{Model: model})
}
