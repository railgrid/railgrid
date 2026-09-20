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
	"strconv"
	"strings"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
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
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"baseURL,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
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
	sec, err := c.GetSecret(ctx, SecretNamespace, strings.TrimSpace(cred.Spec.SecretRef.Name))
	if err != nil {
		return Profile{}, err
	}
	key := APIKeyFromSecret(sec, cred.Spec)
	if key == "" {
		return Profile{}, fmt.Errorf("secret %q has no %q key: %w", cred.Spec.SecretRef.Name, SecretKey(cred.Spec), ErrNotConfigured)
	}
	return Profile{
		Provider: cred.Spec.Provider,
		BaseURL:  cred.Spec.BaseURL,
		Model:    cred.Spec.Model,
		APIKey:   key,
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

// BuildModel constructs an Eino chat model from a profile. Only the
// OpenAI-compatible path is implemented; Gemini and other native providers are
// added later.
func BuildModel(ctx context.Context, p Profile) (einomodel.BaseChatModel, error) {
	p = p.normalized()
	if p.APIKey == "" {
		return nil, ErrNotConfigured
	}
	if p.Model == "" {
		return nil, fmt.Errorf("model credential has no model id")
	}
	switch p.Provider {
	case ProviderOpenAICompatible, ProviderOpenAI, "":
		cfg := &openaimodel.ChatModelConfig{
			APIKey:          p.APIKey,
			BaseURL:         strings.TrimRight(p.BaseURL, "/"),
			Model:           p.Model,
			HTTPClient:      &http.Client{},
			ReasoningEffort: modelReasoningEffort(p.Model),
		}
		if modelSupportsTemperature(p.Model) {
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

// ---- discovery --------------------------------------------------------------

// DiscoverModels calls GET {baseURL}/models and returns the CHAT-CAPABLE
// served model ids plus the round-trip latency. A non-2xx status or transport
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
// speech, embeddings, images, the responses-only families — and this engine
// speaks Chat Completions only, so an uncurated list is a list of ways to
// save a credential that fails on its first turn. Filtering at the single
// shared call is what keeps status.models and the verb's answer identical.
func DiscoverModels(ctx context.Context, baseURL, apiKey string) ([]string, time.Duration, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
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
	if m == "gpt-5.6-luna" {
		return openaimodel.ReasoningEffortLevel("none")
	}
	return ""
}

// modelSupportsTemperature reports whether the model accepts a custom sampling
// temperature. OpenAI's GPT-5 family and o-series reasoning models fix it.
func modelSupportsTemperature(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "gpt5"):
		return false
	case strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return false
	}
	return true
}
