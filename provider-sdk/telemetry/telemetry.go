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

// Package telemetry is the small, standard-library-only client that providers
// use to report bounded product events to the Faros hub. It deliberately has
// no provider or hub package dependency so it can be imported by every
// standalone provider.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultEndpointPath is a format string for the provider-scoped hub
	// endpoint. Provider names are validated before it is used.
	DefaultEndpointPath = "/api/providers/%s/telemetry"

	DefaultQueueSize      = 64
	DefaultEnqueueTimeout = 10 * time.Millisecond
	DefaultSendTimeout    = 2 * time.Second
	DefaultCloseTimeout   = 2 * time.Second

	// These bounds keep event reporting from becoming an unbounded request
	// body or an accidental raw-data transport. The receiver has its own
	// independent limits; local validation fails before an event is queued.
	DefaultMaxEventBytes         = 16 * 1024
	DefaultMaxProperties         = 32
	DefaultMaxPropertyBytes      = 1024
	DefaultMaxPropertyDepth      = 2
	DefaultMaxPropertyCollection = 16
	DefaultMaxIdentifierBytes    = 256
	DefaultMaxCorrelationBytes   = 128
	DefaultMaxActorBytes         = 512
	DefaultMaxProviderNameBytes  = 64
	DefaultMaxActionBytes        = 128
	DefaultMaxEventStringBytes   = 2048
)

var (
	// ErrDisabled is documented for callers that need to identify disabled
	// configuration. Track intentionally does not return it: disabled
	// instrumentation is a successful no-op.
	ErrDisabled = errors.New("telemetry disabled")
	// ErrInvalidConfig indicates that an enabled client cannot be constructed.
	ErrInvalidConfig = errors.New("invalid telemetry configuration")
	// ErrInvalidEvent indicates a locally rejected event.
	ErrInvalidEvent = errors.New("invalid telemetry event")
	// ErrPayloadTooLarge indicates that an event exceeded the local body bound.
	ErrPayloadTooLarge = errors.New("telemetry payload is too large")
	// ErrQueueFull indicates that the bounded enqueue window elapsed.
	ErrQueueFull = errors.New("telemetry queue is full")
	// ErrClosed indicates that a client has been closed.
	ErrClosed = errors.New("telemetry client is closed")
)

var (
	actionPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	propertyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	providerPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// Tracker is the provider-facing telemetry boundary. Implementations return
// promptly after local validation and bounded enqueue; network failures are
// handled by the client worker and do not become product request failures.
type Tracker interface {
	Track(context.Context, Event) error
	Close() error
}

// NoopTracker is the disabled implementation. It performs no validation,
// starts no goroutines, and makes no network calls.
type NoopTracker struct{}

var _ Tracker = NoopTracker{}

// Track implements Tracker.
func (NoopTracker) Track(context.Context, Event) error { return nil }

// Close implements Tracker. There is no worker to stop in no-op mode.
func (NoopTracker) Close() error { return nil }

// Event is the stable provider event contract. Identifiers are opaque stable
// values supplied by the provider; they are not names, URLs, request bodies,
// or credentials. Actor is an input supplied by the caller. Properties are
// intentionally small scalar or shallow collection values, rather than
// arbitrary product payloads.
type Event struct {
	Action        string         `json:"action"`
	OccurredAt    time.Time      `json:"occurred_at"`
	OrgID         string         `json:"org_id,omitempty"`
	WorkspaceID   string         `json:"workspace_id,omitempty"`
	ProjectID     string         `json:"project_id,omitempty"`
	ResourceID    string         `json:"resource_id,omitempty"`
	Actor         string         `json:"actor,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Properties    map[string]any `json:"properties,omitempty"`
}

// Config configures an enabled provider client. Enabled defaults to false so
// providers can construct a tracker in every environment without making a
// network call unless telemetry is explicitly switched on.
type Config struct {
	Enabled       bool
	ProviderName  string
	HubURL        string
	ProviderToken string

	QueueSize      int
	EnqueueTimeout time.Duration
	SendTimeout    time.Duration
	CloseTimeout   time.Duration

	MaxEventBytes    int
	MaxProperties    int
	MaxPropertyBytes int

	// HTTPClient is injectable for tests and provider-specific transport/TLS
	// configuration. Request contexts still enforce SendTimeout.
	HTTPClient *http.Client
}

// Client is the asynchronous HTTP tracker. It owns a bounded queue and one
// sender worker. HTTP responses and errors are intentionally not logged or
// returned from Track; telemetry must not turn a provider product request into
// a telemetry dependency. Close requests worker shutdown within CloseTimeout;
// compliant transports stop immediately, while a non-cooperative injected
// transport cannot delay process shutdown or start another queued event.
type Client struct {
	provider string
	token    string
	endpoint string
	client   *http.Client

	queue            chan []byte
	enqueueTimeout   time.Duration
	sendTimeout      time.Duration
	closeTimeout     time.Duration
	maxEventBytes    int
	maxProperties    int
	maxPropertyBytes int

	workerCtx  context.Context
	cancel     context.CancelFunc
	workerDone chan struct{}

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	enabled   bool
}

var _ Tracker = (*Client)(nil)

// NewClient constructs a Tracker. Disabled configuration returns an inert
// NoopTracker and does not validate the remaining fields or allocate a client.
func NewClient(cfg Config) (Tracker, error) {
	if !cfg.Enabled {
		return NoopTracker{}, nil
	}

	provider := strings.TrimSpace(cfg.ProviderName)
	if !providerPattern.MatchString(provider) || len(provider) > DefaultMaxProviderNameBytes {
		return nil, fmt.Errorf("telemetry: provider name is invalid: %w", ErrInvalidConfig)
	}
	token := strings.TrimSpace(cfg.ProviderToken)
	if token == "" {
		return nil, fmt.Errorf("telemetry: provider bearer token is required: %w", ErrInvalidConfig)
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("telemetry: provider bearer token contains invalid characters: %w", ErrInvalidConfig)
	}

	endpoint, err := providerEndpoint(cfg.HubURL, provider)
	if err != nil {
		return nil, err
	}

	queueSize := cfg.QueueSize
	if queueSize == 0 {
		queueSize = DefaultQueueSize
	}
	if queueSize < 1 || queueSize > 4096 {
		return nil, fmt.Errorf("telemetry: queue size must be between 1 and 4096: %w", ErrInvalidConfig)
	}
	enqueueTimeout := cfg.EnqueueTimeout
	if enqueueTimeout == 0 {
		enqueueTimeout = DefaultEnqueueTimeout
	}
	if enqueueTimeout < 0 || enqueueTimeout > time.Second {
		return nil, fmt.Errorf("telemetry: enqueue timeout must be between 0 and 1s: %w", ErrInvalidConfig)
	}
	sendTimeout := cfg.SendTimeout
	if sendTimeout == 0 {
		sendTimeout = DefaultSendTimeout
	}
	if sendTimeout < 0 || sendTimeout > 10*time.Second {
		return nil, fmt.Errorf("telemetry: send timeout must be between 0 and 10s: %w", ErrInvalidConfig)
	}
	closeTimeout := cfg.CloseTimeout
	if closeTimeout == 0 {
		closeTimeout = DefaultCloseTimeout
	}
	if closeTimeout < 0 || closeTimeout > 10*time.Second {
		return nil, fmt.Errorf("telemetry: close timeout must be between 0 and 10s: %w", ErrInvalidConfig)
	}

	maxEventBytes := cfg.MaxEventBytes
	if maxEventBytes == 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	if maxEventBytes < 1 || maxEventBytes > 1024*1024 {
		return nil, fmt.Errorf("telemetry: max event bytes must be between 1 and 1048576: %w", ErrInvalidConfig)
	}
	maxProperties := cfg.MaxProperties
	if maxProperties == 0 {
		maxProperties = DefaultMaxProperties
	}
	if maxProperties < 1 || maxProperties > 256 {
		return nil, fmt.Errorf("telemetry: max properties must be between 1 and 256: %w", ErrInvalidConfig)
	}
	maxPropertyBytes := cfg.MaxPropertyBytes
	if maxPropertyBytes == 0 {
		maxPropertyBytes = DefaultMaxPropertyBytes
	}
	if maxPropertyBytes < 1 || maxPropertyBytes > 64*1024 {
		return nil, fmt.Errorf("telemetry: max property bytes must be between 1 and 65536: %w", ErrInvalidConfig)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	c := &Client{
		provider:         provider,
		token:            token,
		endpoint:         endpoint,
		client:           httpClient,
		queue:            make(chan []byte, queueSize),
		enqueueTimeout:   enqueueTimeout,
		sendTimeout:      sendTimeout,
		closeTimeout:     closeTimeout,
		maxEventBytes:    maxEventBytes,
		maxProperties:    maxProperties,
		maxPropertyBytes: maxPropertyBytes,
		workerCtx:        workerCtx,
		cancel:           cancel,
		workerDone:       make(chan struct{}),
		enabled:          true,
	}
	go c.run()
	return c, nil
}

func providerEndpoint(rawHubURL, provider string) (string, error) {
	hubURL, err := url.Parse(strings.TrimSpace(rawHubURL))
	if err != nil || hubURL.Scheme == "" || hubURL.Host == "" || hubURL.User != nil {
		return "", fmt.Errorf("telemetry: invalid hub URL: %w", ErrInvalidConfig)
	}
	if hubURL.Scheme != "http" && hubURL.Scheme != "https" {
		return "", fmt.Errorf("telemetry: hub URL scheme must be http or https: %w", ErrInvalidConfig)
	}
	if hubURL.RawQuery != "" || hubURL.Fragment != "" {
		return "", fmt.Errorf("telemetry: hub URL must not contain query or fragment: %w", ErrInvalidConfig)
	}
	hubURL.Path = strings.TrimRight(hubURL.Path, "/") + fmt.Sprintf(DefaultEndpointPath, provider)
	hubURL.RawPath = ""
	return hubURL.String(), nil
}

// Track validates, snapshots, and enqueues an event. It never waits for the
// network. If the bounded queue is full, it waits at most EnqueueTimeout and
// returns ErrQueueFull. A caller context can shorten that enqueue wait.
func (c *Client) Track(ctx context.Context, event Event) error {
	if c == nil || !c.enabled {
		return nil
	}
	payload, err := c.prepareEvent(event)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return ErrClosed
	}
	if c.enqueueTimeout <= 0 {
		select {
		case c.queue <- payload:
			return nil
		default:
			return ErrQueueFull
		}
	}
	timer := time.NewTimer(c.enqueueTimeout)
	defer timer.Stop()
	select {
	case c.queue <- payload:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrQueueFull
	}
}

func (c *Client) prepareEvent(event Event) ([]byte, error) {
	event.Action = strings.TrimSpace(event.Action)
	if !actionPattern.MatchString(event.Action) || len(event.Action) > DefaultMaxActionBytes {
		return nil, fmt.Errorf("telemetry: action must be lowercase snake_case and <=128 bytes: %w", ErrInvalidEvent)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	for name, value := range map[string]string{
		"org_id": event.OrgID, "workspace_id": event.WorkspaceID,
		"project_id": event.ProjectID, "resource_id": event.ResourceID,
	} {
		if err := validateIdentifier(name, value, DefaultMaxIdentifierBytes); err != nil {
			return nil, err
		}
	}
	if err := validateIdentifier("actor", event.Actor, DefaultMaxActorBytes); err != nil {
		return nil, err
	}
	if err := validateIdentifier("correlation_id", event.CorrelationID, DefaultMaxCorrelationBytes); err != nil {
		return nil, err
	}
	if len(event.Properties) > c.maxProperties {
		return nil, fmt.Errorf("telemetry: properties exceed %d entries: %w", c.maxProperties, ErrInvalidEvent)
	}
	for key, value := range event.Properties {
		if !propertyKeyPattern.MatchString(key) || isSensitiveKey(key) {
			return nil, fmt.Errorf("telemetry: property key %q is not an allowed declared key: %w", key, ErrInvalidEvent)
		}
		if err := validateValue(value, c.maxPropertyBytes, DefaultMaxPropertyDepth, true); err != nil {
			return nil, fmt.Errorf("telemetry: property %q: %w", key, err)
		}
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("telemetry: encode event: %w", ErrInvalidEvent)
	}
	if len(payload) > c.maxEventBytes {
		return nil, fmt.Errorf("telemetry: event is %d bytes, limit is %d: %w", len(payload), c.maxEventBytes, ErrPayloadTooLarge)
	}
	return payload, nil
}

func validateIdentifier(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	if len(value) > maxBytes {
		return fmt.Errorf("telemetry: %s exceeds %d bytes: %w", name, maxBytes, ErrInvalidEvent)
	}
	if hasControlOrSpace(value) {
		return fmt.Errorf("telemetry: %s contains whitespace or control characters: %w", name, ErrInvalidEvent)
	}
	return nil
}

func validateValue(value any, maxStringBytes, depth int, property bool) error {
	if value == nil {
		return fmt.Errorf("value must not be null: %w", ErrInvalidEvent)
	}
	switch value := value.(type) {
	case string:
		if len(value) > maxStringBytes || len(value) > DefaultMaxEventStringBytes {
			return fmt.Errorf("string value exceeds %d bytes: %w", maxStringBytes, ErrInvalidEvent)
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("string value contains control characters: %w", ErrInvalidEvent)
		}
		return nil
	case bool:
		return nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("number is not finite: %w", ErrInvalidEvent)
		}
		return nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("number is not finite: %w", ErrInvalidEvent)
		}
		return nil
	case json.Number:
		if _, err := value.Float64(); err != nil {
			return fmt.Errorf("number is invalid: %w", ErrInvalidEvent)
		}
		return nil
	case []byte:
		return fmt.Errorf("byte slices are not allowed: %w", ErrInvalidEvent)
	case []any:
		if !property || depth <= 0 || len(value) > DefaultMaxPropertyCollection {
			return fmt.Errorf("array is not bounded: %w", ErrInvalidEvent)
		}
		for _, item := range value {
			if err := validateValue(item, maxStringBytes, depth-1, true); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if !property || depth <= 0 {
			return fmt.Errorf("object is not bounded: %w", ErrInvalidEvent)
		}
		return validateObject(value, maxStringBytes, depth)
	default:
		return fmt.Errorf("value type %T is not allowed: %w", value, ErrInvalidEvent)
	}
}

func validateObject(value map[string]any, maxStringBytes, depth int) error {
	if len(value) > DefaultMaxPropertyCollection {
		return fmt.Errorf("object has too many fields: %w", ErrInvalidEvent)
	}
	for key, child := range value {
		if !propertyKeyPattern.MatchString(key) || isSensitiveKey(key) {
			return fmt.Errorf("object key %q is not allowed: %w", key, ErrInvalidEvent)
		}
		if err := validateValue(child, maxStringBytes, depth-1, true); err != nil {
			return err
		}
	}
	return nil
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, fragment := range []string{
		"token", "secret", "password", "passwd", "authorization", "cookie",
		"credential", "apikey", "api_key", "private", "email", "phone",
		"address", "tenant", "workspace", "organization", "org", "user",
		"identity", "ip", "url", "uri", "path", "query", "prompt", "message",
		"content", "body", "header", "stack", "trace", "command", "argv", "key",
	} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func hasControlOrSpace(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0
}

func (c *Client) run() {
	defer close(c.workerDone)
	for {
		// Prefer shutdown over draining more work once Close has requested a
		// stop. This prevents a queued event from starting after Close's
		// deadline, even if an injected transport does not honor cancellation.
		select {
		case <-c.workerCtx.Done():
			return
		default:
		}
		select {
		case <-c.workerCtx.Done():
			return
		case payload, ok := <-c.queue:
			if !ok {
				return
			}
			c.send(payload)
		}
		if c.workerCtx.Err() != nil {
			return
		}
	}
}

func (c *Client) send(payload []byte) {
	ctx, cancel := context.WithTimeout(c.workerCtx, c.sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Faros-Provider", c.provider)
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain only a tiny bounded response body to permit connection reuse. Never
	// include response text in an error or log: receivers may echo payload data.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
}

// Enabled reports whether this client owns a sender worker.
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// Provider returns the configured provider name without exposing the token.
func (c *Client) Provider() string {
	if c == nil {
		return ""
	}
	return c.provider
}

// Endpoint returns the configured endpoint. It contains no credentials.
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// Close stops accepting events and requests sender-worker shutdown within the
// configured close deadline. Events still queued at deadline are dropped;
// telemetry is best-effort and must not hold process shutdown hostage.
func (c *Client) Close() error {
	if c == nil || !c.enabled {
		return nil
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.queue)
		c.mu.Unlock()

		timer := time.NewTimer(c.closeTimeout)
		defer timer.Stop()
		select {
		case <-c.workerDone:
			return
		case <-timer.C:
			// Do not wait for a misbehaving injected RoundTripper after the
			// deadline. The worker checks workerCtx before starting another
			// event; compliant transports exit immediately on cancellation.
			c.cancel()
		}
	})
	return nil
}
