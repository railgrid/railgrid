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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

func TestHelmJoinUsesIssuedHubCredentialAndPreservesRuntimeClient(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")
	var observedAuthorization, observedPath string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedAuthorization, observedPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"edges.railgrid.ai/v1alpha1","kind":"PlacementList","metadata":{},"items":[]}`)
	}))
	defer hub.Close()
	a := agentForCredentialTest("helm-edge", hub.URL)
	a.hubConfig = &rest.Config{Host: hub.URL, BearerToken: "bootstrap-join-token"}
	a.downstreamConfig = &rest.Config{Host: "https://runtime.invalid", BearerToken: "runtime-service-account"}
	runtimeConfig := rest.CopyConfig(a.downstreamConfig)
	a.credentials = &tunnel.CredentialStore{}
	initial, err := railgridclient.NewForConfig(a.hubConfig)
	if err != nil {
		t.Fatal(err)
	}
	issued := tunnel.Credential{Token: "issued-agent-token", HubURL: hub.URL, ClusterID: "tenant-cluster", Resource: "kubernetesclusters", Name: "helm-edge", ExpiresAt: time.Now().Add(time.Hour)}
	enrolled := make(chan struct{})
	go func() {
		if err := a.credentials.Adopt(issued); err != nil {
			t.Error(err)
		}
		close(enrolled)
	}()
	client, err := a.hubClientAfterEnrollment(context.Background(), initial, enrolled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dynamic().Resource(railgridclient.PlacementGVR).List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if observedAuthorization != "Bearer issued-agent-token" || !strings.HasPrefix(observedPath, "/clusters/tenant-cluster/") {
		t.Fatalf("hub reporter did not use the issued tenant credential: path=%s", observedPath)
	}
	if a.downstreamConfig.Host != runtimeConfig.Host || a.downstreamConfig.BearerToken != runtimeConfig.BearerToken {
		t.Fatal("hub credential transition changed the runtime cluster client")
	}
}

func TestHelmJoinCancellationStopsCredentialWait(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")
	a := agentForCredentialTest("helm-edge", "https://hub.invalid")
	a.credentials = &tunnel.CredentialStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.hubClientAfterEnrollment(ctx, nil, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("enrollment wait ignored cancellation: %v", err)
	}
}

func TestRestartWithSavedCredentialUsesItBeforeEdgeRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const edgeName = "linux-restart"
	const sshCredentialsPath = "/clusters/tenant-cluster/apis/edges.railgrid.ai/v1alpha1/linuxservers/linux-restart/ssh-credentials"
	type observedRequest struct {
		path          string
		authorization string
		credentials   tunnel.SSHCredentials
	}
	gotRequests := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := observedRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read SSH credential request body: %v", err)
		} else if err := json.Unmarshal(payload, &observed.credentials); err != nil {
			t.Errorf("decode SSH credential request body: %v", err)
		}
		gotRequests <- observed
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hubURL := server.URL
	issued := tunnel.Credential{
		Token:              "synthetic-issued-test-token",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(time.Hour),
		HubURL:             hubURL,
		Provider:           "edges-connectivity",
		ClusterID:          "tenant-cluster",
		Resource:           "linuxservers",
		Name:               edgeName,
		SSHCredentialsPath: sshCredentialsPath,
	}
	if err := SaveAgentCredential(edgeName, issued); err != nil {
		t.Fatalf("saving test credential: %v", err)
	}

	options := NewOptions()
	options.EdgeName = edgeName
	options.HubURL = hubURL
	// Installed service arguments may retain the bootstrap token after the
	// first successful enrollment. The saved scoped credential takes precedence
	// on restart, so it must still be usable for operator credential rotation.
	options.Token = "stale-bootstrap-token"
	options.Type = AgentTypeServer
	options.SSHUser = "rotation-user"
	options.SSHPassword = "rotated-on-restart"
	options.InsecureSkipTLSVerify = true

	agent, err := New(options)
	if err != nil {
		t.Fatalf("creating agent for restart: %v", err)
	}

	if !agent.hasIssuedCredential() {
		t.Fatal("restart did not adopt the saved issued credential")
	}
	if agent.shouldRegisterEdge() {
		t.Fatal("restart with an issued credential must skip edge registration")
	}
	if !agent.shouldSetupSSHCredentials() {
		t.Fatal("restart with an issued credential must remain eligible to submit operator SSH credential rotations")
	}
	if agent.hubConfig.BearerToken != "" || agent.hubConfig.WrapTransport == nil {
		t.Fatal("restart hub client did not use the rotating issued credential transport")
	}
	if want := apiurl.HubServerURL(hubURL, issued.ClusterID); agent.hubConfig.Host != want {
		t.Fatalf("restart hub client host = %q, want credential workspace host %q", agent.hubConfig.Host, want)
	}
	if agent.hubTLSConfig == nil || !agent.hubTLSConfig.InsecureSkipVerify {
		t.Fatal("restart tunnel TLS config was not rebuilt from the saved credential settings")
	}
	if agent.credentials.TLSConfig != agent.hubTLSConfig {
		t.Fatal("credential refresh client and hub config do not share the restored TLS settings")
	}

	agent.credentials.HTTPClient = server.Client()
	if err := agent.setupSSHCredentials(context.Background(), klog.Background(), nil); err != nil {
		t.Fatalf("submit rotated SSH credentials with the saved agent credential: %v", err)
	}
	var gotRequest observedRequest
	select {
	case gotRequest = <-gotRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("rotated SSH credentials were not uploaded")
	}
	if gotRequest.path != sshCredentialsPath {
		t.Fatalf("SSH credential request path = %q, want %q", gotRequest.path, sshCredentialsPath)
	}
	if gotRequest.authorization != "Bearer "+issued.Token {
		t.Fatalf("SSH credential request authorization = %q, want the saved issued credential", gotRequest.authorization)
	}
	if gotRequest.credentials.Username != options.SSHUser || gotRequest.credentials.Password != options.SSHPassword {
		t.Fatalf("uploaded SSH credentials = %+v, want the operator's rotated username and password", gotRequest.credentials)
	}
}

// A pod restart is the case the in-cluster Secret exists for: the agent's
// filesystem is gone, so the enrolment bundle can only come from the Secret.
// Reading only the file made the agent fall back to its bootstrap join token on
// every pod restart, which the hub refuses once that token has been rotated or
// orphaned -- the edge then stays Disconnected for good.
func TestPodRestartLoadsTheCredentialFromItsSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a fresh pod: no credential on disk
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-restart"
	stored := tunnel.Credential{
		Token:     "credential-from-the-secret",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    "https://hub.example.com",
		ClusterID: "tenant-cluster",
		Resource:  "kubernetesclusters",
		Name:      edgeName,
	}
	secretReads := 0
	stubSecretLoader(t, func(name string) (tunnel.Credential, bool, error) {
		secretReads++
		if name != edgeName {
			t.Errorf("secret loader called for %q, want %q", name, edgeName)
		}
		return stored, true, nil
	})

	store := agentForCredentialTest(edgeName, stored.HubURL).newCredentialStore()

	if secretReads == 0 {
		t.Fatal("the agent never looked in its Secret; a pod restart would fall back to the join token")
	}
	adopted, ok := store.Current()
	if !ok {
		t.Fatal("pod restart did not adopt the credential stored in the Secret")
	}
	if adopted.Token != stored.Token {
		t.Fatalf("adopted token = %q, want the credential from the Secret", adopted.Token)
	}
	if store.Token() != stored.Token {
		t.Fatalf("store serves %q, want the credential from the Secret rather than the join token", store.Token())
	}
}

// The file still wins when it is there, and the Secret is not consulted at all:
// outside a pod there is no Secret to read, and inside one the two copies are
// written together.
func TestSavedCredentialOnDiskIsPreferredToTheSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-disk-wins"
	onDisk := tunnel.Credential{
		Token:     "credential-from-disk",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    "https://hub.example.com",
		ClusterID: "tenant-cluster",
		Resource:  "kubernetesclusters",
		Name:      edgeName,
	}
	if err := SaveAgentCredential(edgeName, onDisk); err != nil {
		t.Fatalf("saving test credential: %v", err)
	}
	stubSecretLoader(t, func(string) (tunnel.Credential, bool, error) {
		t.Error("the Secret was read although a credential was present on disk")
		return tunnel.Credential{}, false, nil
	})

	store := agentForCredentialTest(edgeName, onDisk.HubURL).newCredentialStore()

	if store.Token() != onDisk.Token {
		t.Fatalf("store serves %q, want the credential from disk", store.Token())
	}
}

// An unreadable credential on disk must still be reported when the Secret has
// nothing to offer either. Swallowing it would drop the only diagnostic the
// operator gets before the agent falls back to its join token, and a corrupt
// file looks exactly like an agent that has never enrolled.
func TestUnreadableCredentialOnDiskIsReportedWhenTheSecretIsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-corrupt-file"
	originalFileLoader := loadAgentCredential
	loadAgentCredential = func(string) (tunnel.Credential, bool, error) {
		return tunnel.Credential{}, false, errors.New("unexpected end of JSON input")
	}
	t.Cleanup(func() { loadAgentCredential = originalFileLoader })
	stubSecretLoader(t, func(string) (tunnel.Credential, bool, error) {
		return tunnel.Credential{}, false, nil // Secret exists but holds no bundle
	})

	_, _, ok, err := agentForCredentialTest(edgeName, "https://hub.example.com").loadSavedCredential(edgeName)
	if ok {
		t.Fatal("a credential was reported although neither store had one")
	}
	if err == nil {
		t.Fatal("the unreadable file was swallowed; the operator gets no diagnostic")
	}
	if !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("error %q does not mention the underlying file failure", err)
	}
}

// But a *missing* file is the normal case on a pod restart, so it must stay
// quiet when the Secret supplies the credential.
func TestMissingCredentialFileIsNotAnErrorWhenTheSecretHasOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-missing-file"
	stored := tunnel.Credential{
		Token:     "credential-from-the-secret",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    "https://hub.example.com",
		Name:      edgeName,
	}
	stubSecretLoader(t, func(string) (tunnel.Credential, bool, error) {
		return stored, true, nil
	})

	_, source, ok, err := agentForCredentialTest(edgeName, stored.HubURL).loadSavedCredential(edgeName)
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if !ok || source != "secret" {
		t.Fatalf("ok=%v source=%q, want true/\"secret\"", ok, source)
	}
}

// agentForCredentialTest builds the minimum Agent newCredentialStore needs.
// Going through New() would also build a downstream cluster config, which is
// unrelated to credential loading and fails wherever no kubeconfig exists.
func agentForCredentialTest(edgeName, hubURL string) *Agent {
	options := NewOptions()
	options.EdgeName = edgeName
	options.HubURL = hubURL
	// Installed service arguments keep the bootstrap token after enrolment; the
	// saved credential must win over it.
	options.Token = "stale-bootstrap-token"
	options.Type = AgentTypeKubernetes
	options.InsecureSkipTLSVerify = true
	return &Agent{opts: options}
}

// stubSecretLoader swaps the in-cluster Secret loader for the duration of a
// test; the real one builds its own in-cluster client.
func stubSecretLoader(t *testing.T, fn func(string) (tunnel.Credential, bool, error)) {
	t.Helper()
	original := loadCredentialFromSecret
	loadCredentialFromSecret = fn
	t.Cleanup(func() { loadCredentialFromSecret = original })
}

// A capability the hub momentarily cannot serve must not be given up on. The
// SSH-credential handover used to be fatal, so a 500 from the provider took the
// whole agent down and systemd restarted it into the same failure; it now
// retries in the background while the edge serves everything else.
func TestRetryUntilSuccessKeepsTryingAndStops(t *testing.T) {
	attempts := 0
	retryUntilSuccess(t.Context(), klog.Background(), time.Millisecond, 5*time.Millisecond, "test",
		func(context.Context) error {
			attempts++
			if attempts < 3 {
				return errors.New("HTTP 500")
			}
			return nil
		})
	if attempts != 3 {
		t.Fatalf("attempts = %d, want it to keep trying until it succeeded (3)", attempts)
	}
}

// Shutdown is the one thing that ends the retry.
func TestRetryUntilSuccessStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		retryUntilSuccess(ctx, klog.Background(), time.Millisecond, 2*time.Millisecond, "test",
			func(context.Context) error {
				attempts++
				if attempts == 2 {
					cancel()
				}
				return errors.New("still failing")
			})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retryUntilSuccess did not return after its context was cancelled")
	}
}

func TestNativeHubClientUsesRotatedIssuedTokenWithoutBootstrapFallback(t *testing.T) {
	var observed []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = append(observed, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"edges.railgrid.ai/v1alpha1","kind":"PlacementList","items":[]}`)
	}))
	defer hub.Close()
	a := agentForCredentialTest("rotating-edge", hub.URL)
	a.opts.Token = "bootstrap-join-token"
	a.credentials = &tunnel.CredentialStore{Fallback: func() string { return a.opts.Token }}
	credential := tunnel.Credential{Token: "initial-issued", HubURL: hub.URL, ClusterID: "tenant", Resource: "kubernetesclusters", Name: "rotating-edge", ExpiresAt: time.Now().Add(time.Hour)}
	if err := a.credentials.Adopt(credential); err != nil {
		t.Fatal(err)
	}
	client, err := a.hubClientAfterEnrollment(context.Background(), nil, make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	list := func() {
		t.Helper()
		if _, err := client.Dynamic().Resource(railgridclient.PlacementGVR).List(context.Background(), metav1.ListOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	list()
	credential.Token = "fresh-issued"
	if err := a.credentials.Adopt(credential); err != nil {
		t.Fatal(err)
	}
	list()
	if !a.credentials.Rejected() {
		t.Fatal("test did not activate join-token fallback")
	}
	if a.credentials.Token() != a.opts.Token {
		t.Fatal("test store is not presenting its bootstrap fallback")
	}
	list()
	if len(observed) != 3 || observed[0] != "Bearer initial-issued" || observed[1] != "Bearer fresh-issued" || observed[2] != "Bearer fresh-issued" {
		t.Fatalf("native client did not follow issued tokens independently of join fallback: %v", observed)
	}
	// An in-memory credential from another tenant/hub must not be sent through
	// the old client's address or cause its TLS configuration to be rewritten.
	for _, change := range []string{"cluster", "hub"} {
		changed := credential
		if change == "cluster" {
			changed.ClusterID = "other-tenant"
		} else {
			changed.HubURL = "https://other.invalid"
		}
		if err := a.credentials.Adopt(changed); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Dynamic().Resource(railgridclient.PlacementGVR).List(context.Background(), metav1.ListOptions{}); err == nil {
			t.Fatalf("accepted changed %s addressing", change)
		}
	}
	if len(observed) != 3 {
		t.Fatal("a credential with changed addressing reached the old hub")
	}
	if a.hubConfig.Host != apiurl.HubServerURL(hub.URL, "tenant") {
		t.Fatal("rotation changed the existing client address")
	}
}

func TestNativeHubClientRefreshesWithoutTunnelReconnect(t *testing.T) {
	refreshes, reads := 0, 0
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/refresh" {
			refreshes++
			if r.Header.Get("Authorization") != "Bearer due-issued" {
				t.Error("refresh did not use the issued token")
			}
			_ = json.NewEncoder(w).Encode(tunnel.Credential{Token: "refreshed-issued", HubURL: hub.URL, ClusterID: "tenant", Resource: "kubernetesclusters", Name: "refreshing-edge", RefreshPath: "/refresh", ExpiresAt: time.Now().Add(time.Hour)})
			return
		}
		reads++
		if r.Header.Get("Authorization") != "Bearer refreshed-issued" {
			t.Error("native request did not use the refreshed token")
		}
		_, _ = io.WriteString(w, `{"apiVersion":"edges.railgrid.ai/v1alpha1","kind":"PlacementList","items":[]}`)
	}))
	defer hub.Close()
	a := agentForCredentialTest("refreshing-edge", hub.URL)
	a.credentials = &tunnel.CredentialStore{}
	if err := a.credentials.Adopt(tunnel.Credential{Token: "due-issued", HubURL: hub.URL, ClusterID: "tenant", Resource: "kubernetesclusters", Name: "refreshing-edge", RefreshPath: "/refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	client, err := a.refreshHubClientFromCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dynamic().Resource(railgridclient.PlacementGVR).List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || reads != 1 {
		t.Fatalf("refresh calls=%d native reads=%d", refreshes, reads)
	}
}
