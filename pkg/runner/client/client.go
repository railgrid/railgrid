/*
Copyright 2026 The Railgrid Authors.

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

// Package client reaches one enrolled railgrid runner through the edges
// provider's published Service proxy, speaking the typed runner/v1 protocol in
// pkg/runner.
//
// It exists once, next to the protocol types, because every provider that
// dispatches to a runner needs the same three things and must not get any of
// them subtly different: a transport that cannot be downgraded, a coordinate
// that is rendered by the one renderer of the data-plane grammar, and an
// identity check that refuses a runner which is not the runner the caller was
// enrolled with.
//
// It owns no scheduler, mints no credentials, and holds no execution identity:
// the *rest.Config handed to New is the caller's own workspace identity, and
// every request this package makes is evaluated against it.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/runner"
)

// Response bounds. A runner is a machine someone else administers, so every
// read from it is bounded: a caller that runs out of memory because a runner
// answered with a terabyte has lost the argument before it starts.
const (
	// jsonLimit bounds a protocol JSON document (a Receipt, Capabilities, or
	// an error body), in both directions.
	jsonLimit = 2 << 20
	// artifactLimit bounds one artifact download.
	artifactLimit = 32 << 20
	// eventStreamLimit bounds one event stream, across all of its events.
	eventStreamLimit = 64 << 20
)

// requestTimeout bounds one request end to end, including the body. The event
// stream fits inside it because the runner ends a stream itself after 30s of
// silence (pkg/runner/http.go serveEvents), so a caller resumes from its last
// cursor rather than holding a connection open forever.
const requestTimeout = 5 * time.Minute

// discoveryTTL is how long a validated proxy coordinate is reused.
//
// The alternative — re-reading the Service and re-checking the runner identity
// before every operation — costs two round trips per dispatch, which a busy
// coordinator pays on every Start. The window is short, and it is not the only
// protection: the coordinate is invalidated the moment an operation is answered
// with 401/403/404, which is exactly what a Service that was deleted,
// re-pointed, or un-granted produces. See Invalidate.
const discoveryTTL = 30 * time.Second

// identifier is the shape of every protocol identity that reaches a URL path or
// a request body. It is deliberately narrower than a kube name: no dot
// segments, no separators, nothing that can be percent-decoded into either.
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ServiceRef addresses one enrolled runner.
//
// The four coordinates are operator-owned enrollment, never task input: the
// tenant cluster, the edges Service the runner is published as, the edge that
// Service must still point at, and the runner identity that must answer at the
// far end. All four are checked before any operation is sent.
type ServiceRef struct {
	// Cluster is the tenant logical cluster the Service lives in.
	Cluster string
	// Service is the name of the edges Service the runner is published as.
	Service string
	// EdgeKind and EdgeName are the edge the Service must still point at.
	EdgeKind string
	EdgeName string
	// RunnerID is the identity the runner must report for itself.
	RunnerID string
}

// Client invokes one enrolled runner. It is safe for concurrent use.
type Client struct {
	ref    ServiceRef
	http   *http.Client
	origin string

	// mu guards the cached coordinate only; requests are made outside it.
	mu      sync.Mutex
	base    string
	basedAt time.Time
	ttl     time.Duration
	nowFn   func() time.Time
}

// Option adjusts how New validates the transport. There is deliberately no
// option that relaxes the credential, the proxy, the impersonation or the
// certificate rules: those are about WHO the call is made as, and a caller that
// wants them relaxed wants a different call.
type Option func(*options)

type options struct{ allowInsecureTLS bool }

// AllowInsecureTLS accepts a config with TLS verification disabled.
//
// It exists because refusing it here can be inconsistent rather than safer. A
// provider is normally configured with the hub's CA, and then this never
// applies. But an operator who has deliberately pointed a provider at a hub with
// verification off — a development stack with a self-signed certificate, which
// is what RAILGRID_HUB_INSECURE means — has already made that choice for every
// other call that provider makes to the same hub, including the ones that carry
// its own identity. Refusing only the runner hop would not protect the harness
// credential; it would just make the feature unusable on that stack while the
// same channel carried everything else.
//
// It must be passed EXPLICITLY, by a caller that knows its own config is
// insecure. Nothing here infers it.
func AllowInsecureTLS() Option {
	return func(o *options) { o.allowInsecureTLS = true }
}

// New validates the transport and the enrollment and returns a client. It
// performs no I/O: discovery happens on the first operation, so constructing a
// client for a runner that is asleep is not itself an error.
//
// The config must be the caller's own workspace identity addressing the
// enrolled cluster's front door exactly, and nothing about it may weaken the
// connection: no plaintext or unverified TLS, no proxy, no exec or auth
// provider, no basic auth, no impersonation, no client certificate, and no
// redirects. A config carrying no credential at all is refused too — a bearer,
// a bearer file, or a WrapTransport that attaches a hub-minted, TTL'd token per
// request are the three shapes accepted.
//
// Each of those is refused with its own message. They are unrelated faults with
// unrelated fixes, and one sentence covering all of them tells a reader that
// something is wrong with their config without saying what.
func New(cfg *rest.Config, ref ServiceRef, opts ...Option) (*Client, error) {
	var options options
	for _, apply := range opts {
		apply(&options)
	}
	if cfg == nil {
		return nil, errors.New("runner client: a tenant rest.Config is required")
	}
	for _, value := range []string{ref.RunnerID, ref.Cluster, ref.Service, ref.EdgeName} {
		if !identifier.MatchString(value) {
			return nil, errors.New("runner client: enrollment requires valid runner, cluster, service, and edge identities")
		}
	}
	if ref.EdgeKind != EdgeKindMacOSServer && ref.EdgeKind != EdgeKindLinuxServer {
		return nil, fmt.Errorf("runner client: a runner is enrolled on a host edge (%s or %s), not %q", EdgeKindLinuxServer, EdgeKindMacOSServer, ref.EdgeKind)
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/clusters/"+ref.Cluster {
		return nil, errors.New("runner client: the tenant kubeconfig must address the enrolled HTTPS cluster exactly (https://host/clusters/" + ref.Cluster + ")")
	}
	if cfg.Insecure && !options.allowInsecureTLS {
		return nil, errors.New("runner client: this connection carries a harness credential, so unverified TLS is refused; supply the hub's CA, or pass AllowInsecureTLS if the caller already reaches this hub that way")
	}
	if cfg.Proxy != nil {
		return nil, errors.New("runner client: a proxy is refused; a runner call must go to the hub the enrollment names and nowhere else")
	}
	if cfg.ExecProvider != nil || cfg.AuthProvider != nil {
		return nil, errors.New("runner client: an exec or auth provider is refused; the credential must be one this process already holds, not one a helper mints on demand")
	}
	if cfg.Username != "" || cfg.Password != "" {
		return nil, errors.New("runner client: basic auth is refused; a runner call is made as a workspace identity, not as a user with a password")
	}
	if cfg.Impersonate.UserName != "" || len(cfg.Impersonate.Groups) != 0 || len(cfg.Impersonate.Extra) != 0 {
		return nil, errors.New("runner client: impersonation is refused; the call must be attributable to the identity making it")
	}
	if cfg.CertFile != "" || len(cfg.CertData) != 0 || cfg.KeyFile != "" || len(cfg.KeyData) != 0 {
		return nil, errors.New("runner client: a client certificate is refused; the credential must be a bearer so it can be TTL'd and revoked")
	}
	if cfg.BearerToken == "" && cfg.BearerTokenFile == "" && cfg.WrapTransport == nil {
		return nil, errors.New("runner client: the config carries no credential; supply a bearer token, a bearer token file, or a WrapTransport that attaches one per request")
	}
	safe := rest.CopyConfig(cfg)
	safe.Timeout = requestTimeout
	httpClient, err := rest.HTTPClientFor(safe)
	if err != nil {
		return nil, err
	}
	// A redirect would re-send the tenant bearer to a host nobody validated,
	// including on the requests that merely discover the Service.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("runner transport redirects are forbidden")
	}
	return &Client{
		ref:    ref,
		http:   httpClient,
		origin: u.Scheme + "://" + u.Host,
		ttl:    discoveryTTL,
		nowFn:  time.Now,
	}, nil
}

// Invalidate drops the cached proxy coordinate so the next operation
// re-discovers and re-checks it. It is called automatically when an operation is
// answered with 401, 403, or 404 — the statuses a deleted, re-pointed, or
// un-granted Service produces — and is exported so a caller that learns of a
// change some other way can force the same re-check.
//
// Nothing is retried on invalidation: a mutation that failed has an unknown
// outcome, and re-sending it is the coordinator's decision to make with Inspect,
// not this package's.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base = ""
	c.basedAt = time.Time{}
}

func (c *Client) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now()
}

func (c *Client) cachedBase() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.base == "" || c.now().Sub(c.basedAt) >= c.ttl {
		return ""
	}
	return c.base
}

// discover returns the validated proxy coordinate, reading it from the cache
// when it is still fresh.
//
// A cold discovery is three checks, in this order, because each one makes the
// next meaningful: the Service object says it is published at the coordinate
// this package renders and still points at the enrolled edge; then the runner
// behind that coordinate says it is the enrolled runner and speaks this
// protocol. Only then may an operation be sent.
func (c *Client) discover(ctx context.Context) (string, error) {
	if base := c.cachedBase(); base != "" {
		return base, nil
	}
	servicePath, err := servicePath(c.ref)
	if err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodGet, servicePath, nil)
	if err != nil {
		return "", err
	}
	var service unstructured.Unstructured
	if err := readJSON(resp, &service.Object); err != nil {
		return "", err
	}
	base, err := publishedProxy(c.ref, &service)
	if err != nil {
		return "", err
	}
	if _, err := c.capabilities(ctx, base); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.base, c.basedAt = base, c.now()
	c.mu.Unlock()
	return base, nil
}

// servicePath is the ordinary kube read path of the Service object itself. It is
// not a verb, so it is spelled as the resource path it is; the coordinate that
// reaches the runner is rendered by proxyPath.
func servicePath(ref ServiceRef) (string, error) {
	if !identifier.MatchString(ref.Cluster) || !identifier.MatchString(ref.Service) {
		return "", errors.New("runner client: invalid cluster or service identity")
	}
	// Rendered from the same grammar so a cluster or name that could not be a
	// coordinate is refused here too, then trimmed back to the object path.
	proxy, err := proxyPath(ref.Cluster, ref.Service)
	if err != nil {
		return "", err
	}
	return proxy[:len(proxy)-len("/"+edgesProxyVerb)], nil
}

// capabilities reads the runner's capabilities through base and verifies that
// the runner which answered is the enrolled one.
func (c *Client) capabilities(ctx context.Context, base string) (runner.Capabilities, error) {
	resp, err := c.do(ctx, http.MethodGet, base+"/runner/v1/capabilities", nil)
	if err != nil {
		return runner.Capabilities{}, err
	}
	var caps runner.Capabilities
	if err := readJSON(resp, &caps); err != nil {
		return runner.Capabilities{}, err
	}
	if caps.ProtocolVersion != runner.ProtocolVersion || caps.RunnerID != c.ref.RunnerID {
		return runner.Capabilities{}, &IdentityError{Reported: caps.RunnerID, Enrolled: c.ref.RunnerID, Protocol: caps.ProtocolVersion}
	}
	return caps, nil
}

// Capabilities reports what the enrolled runner can do right now. The
// coordinate may be cached but the capabilities never are: readiness and
// capacity are what a caller asks this question to learn.
func (c *Client) Capabilities(ctx context.Context) (runner.Capabilities, error) {
	base, err := c.discover(ctx)
	if err != nil {
		return runner.Capabilities{}, err
	}
	return c.capabilities(ctx, base)
}

// Start dispatches an approved attempt. A failure is never retried here: the
// outcome of a failed mutation is unknown, and Inspect is the authority.
func (c *Client) Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	if err := stampProtocol(&req.ProtocolVersion); err != nil {
		return runner.Receipt{}, err
	}
	if err := validateMutation(req.RequestID, req.TaskID, req.AttemptID, req.AttemptEpoch); err != nil {
		return runner.Receipt{}, err
	}
	return c.mutate(ctx, "/runner/v1/attempts", req)
}

// Inspect reads the durable receipt of one attempt. It is the reconciliation
// path: whenever a mutation or a stream fails with an unknown outcome, the
// receipt is what says what actually happened.
func (c *Client) Inspect(ctx context.Context, attemptID string) (runner.Receipt, error) {
	base, err := c.attemptBase(ctx, attemptID)
	if err != nil {
		return runner.Receipt{}, err
	}
	resp, err := c.do(ctx, http.MethodGet, base, nil)
	if err != nil {
		return runner.Receipt{}, err
	}
	var receipt runner.Receipt
	if err := readJSON(resp, &receipt); err != nil {
		return runner.Receipt{}, err
	}
	return receipt, nil
}

// Cancel asks the runner to stop an attempt.
func (c *Client) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	if err := stampProtocol(&req.ProtocolVersion); err != nil {
		return runner.Receipt{}, err
	}
	if err := validateMutation(req.RequestID, req.TaskID, req.AttemptID, req.AttemptEpoch); err != nil {
		return runner.Receipt{}, err
	}
	base, err := c.attemptBase(ctx, req.AttemptID)
	if err != nil {
		return runner.Receipt{}, err
	}
	return c.post(ctx, base+"/cancel", req)
}

// Resume continues an attempt that is waiting for input.
func (c *Client) Resume(ctx context.Context, req runner.ResumeRequest) (runner.Receipt, error) {
	if err := stampProtocol(&req.ProtocolVersion); err != nil {
		return runner.Receipt{}, err
	}
	if err := validateMutation(req.RequestID, req.TaskID, req.AttemptID, req.AttemptEpoch); err != nil {
		return runner.Receipt{}, err
	}
	base, err := c.attemptBase(ctx, req.AttemptID)
	if err != nil {
		return runner.Receipt{}, err
	}
	return c.post(ctx, base+"/resume", req)
}

// Artifact copies one artifact's bytes into w and returns its metadata as the
// runner served it.
//
// The digest is recomputed while copying and a mismatch is an error: an artifact
// is immutable by contract, and a body that does not hash to the digest the
// runner announced is not that artifact. Only ID, Digest, Length, and MediaType
// are carried on the wire; Name and CreatedAt live on the receipt, so they are
// left zero here rather than invented.
func (c *Client) Artifact(ctx context.Context, attemptID, artifactID string, w io.Writer) (runner.Artifact, error) {
	if w == nil {
		return runner.Artifact{}, errors.New("runner client: an artifact writer is required")
	}
	if !identifier.MatchString(artifactID) {
		return runner.Artifact{}, errors.New("runner client: a valid artifact ID is required")
	}
	base, err := c.attemptBase(ctx, attemptID)
	if err != nil {
		return runner.Artifact{}, err
	}
	resp, err := c.do(ctx, http.MethodGet, base+"/artifacts/"+artifactID, nil)
	if err != nil {
		return runner.Artifact{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(w, digest), io.LimitReader(resp.Body, artifactLimit+1))
	if err != nil {
		return runner.Artifact{}, err
	}
	if written > artifactLimit {
		return runner.Artifact{}, errors.New("runner client: artifact exceeds the 32MiB limit; reconcile with Inspect")
	}
	got := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if announced := resp.Header.Get("Digest"); announced != "" && announced != got {
		return runner.Artifact{}, fmt.Errorf("runner client: artifact %q hashes to %s but the runner announced %s", artifactID, got, announced)
	}
	return runner.Artifact{
		ID:        artifactID,
		Digest:    got,
		Length:    written,
		MediaType: resp.Header.Get("Content-Type"),
	}, nil
}

// Events streams an attempt's events from after (exclusive). A caller resumes
// with the last cursor it saw; passing 0 replays from the beginning of what the
// runner still holds.
//
// The caller must Close the returned stream.
func (c *Client) Events(ctx context.Context, attemptID string, after uint64) (*EventStream, error) {
	base, err := c.attemptBase(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, base+"/events?after="+strconv.FormatUint(after, 10), nil)
	if err != nil {
		return nil, err
	}
	return newEventStream(resp.Body, after), nil
}

// attemptBase discovers the coordinate and appends one attempt's path.
func (c *Client) attemptBase(ctx context.Context, attemptID string) (string, error) {
	if !identifier.MatchString(attemptID) {
		return "", errors.New("runner client: a valid attempt ID is required")
	}
	base, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return base + "/runner/v1/attempts/" + attemptID, nil
}

func (c *Client) mutate(ctx context.Context, path string, body any) (runner.Receipt, error) {
	base, err := c.discover(ctx)
	if err != nil {
		return runner.Receipt{}, err
	}
	return c.post(ctx, base+path, body)
}

func (c *Client) post(ctx context.Context, path string, body any) (runner.Receipt, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return runner.Receipt{}, err
	}
	if len(encoded) > jsonLimit {
		return runner.Receipt{}, errors.New("runner client: the request exceeds the 2MiB protocol limit")
	}
	resp, err := c.do(ctx, http.MethodPost, path, encoded)
	if err != nil {
		return runner.Receipt{}, err
	}
	var receipt runner.Receipt
	if err := readJSON(resp, &receipt); err != nil {
		return runner.Receipt{}, err
	}
	return receipt, nil
}

// do performs one request and returns a response only for a 2xx. The body of a
// failed response is read, bounded, and turned into the most specific error
// available; the caller therefore never has to close a body it did not get.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runner transport failed; Inspect before retrying a mutation: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		failure, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			// The cached coordinate may be the reason: a Service that was
			// deleted, re-pointed, or un-granted answers exactly like this.
			// Drop it so the next operation re-discovers and re-checks.
			c.Invalidate()
		}
		return nil, responseError(resp.StatusCode, failure)
	}
	return resp, nil
}

// readJSON decodes a bounded protocol document and closes the body.
func readJSON(resp *http.Response, target any) error {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, jsonLimit+1))
	if err != nil {
		return err
	}
	if len(body) > jsonLimit {
		return errors.New("runner client: the response exceeds the 2MiB protocol limit")
	}
	return json.Unmarshal(body, target)
}

// stampProtocol fills in the protocol version this package implements and
// refuses any other. A caller cannot usefully choose it — the types it just
// populated ARE runner/v1 — and the runner rejects a request that omits it, so
// stamping it is the difference between a typed client and a footgun.
func stampProtocol(version *string) error {
	if *version == "" {
		*version = runner.ProtocolVersion
		return nil
	}
	if *version != runner.ProtocolVersion {
		return fmt.Errorf("runner client: this client speaks %s, not %q", runner.ProtocolVersion, *version)
	}
	return nil
}

// validateMutation refuses a mutation that is missing the identity the runner
// uses to make it idempotent. Without all four, a retry after a transport
// failure cannot be recognised as the same request, which is how one approved
// attempt becomes two executions.
func validateMutation(requestID, taskID, attemptID string, epoch uint64) error {
	if !identifier.MatchString(requestID) || !identifier.MatchString(taskID) || !identifier.MatchString(attemptID) || epoch == 0 {
		return errors.New("runner client: a mutation requires a full request/task/attempt identity and a nonzero attempt epoch")
	}
	return nil
}
