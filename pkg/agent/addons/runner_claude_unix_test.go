//go:build unix

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

package addons

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	testOAuthToken = "sk-ant-oat01-tenant-token"
	testAPIKey     = "sk-ant-api03-tenant-key"
)

// redirectingClient sends every probe to target regardless of the loopback
// address the add-on built, so a test can serve a capabilities document without
// binding the add-on's configured port.
func redirectingClient(target string) *http.Client {
	base, _ := url.Parse(target)
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			redirected := r.Clone(r.Context())
			redirected.URL.Scheme = base.Scheme
			redirected.URL.Host = base.Host
			return http.DefaultTransport.RoundTrip(redirected)
		}),
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func claudeSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "claude-auth", Namespace: "default"},
		Data:       data,
	}
}

// claudeRunnerSpec is runnerSpec with the harness switched to Claude Code.
func claudeRunnerSpec() Spec {
	spec := runnerSpec()
	spec.Runner.Codex = nil
	spec.Runner.Harness = HarnessClaude
	spec.Runner.Claude = &Claude{
		Binary:         "claude",
		Model:          "sonnet",
		AuthSecretRef:  &SecretRef{Name: "claude-auth", Namespace: "default"},
		PermissionMode: "bypassPermissions",
		AllowedTools:   []string{"Bash(git *)", " "},
	}
	return spec
}

// TestClaudeCredentialIsMaterializedOwnerOnly: the credential is the whole
// authentication story for this harness, so it lands in a 0600 file under the
// add-on's own state directory and nowhere else.
func TestClaudeCredentialIsMaterializedOwnerOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		data map[string][]byte
		want string
		kind string
	}{
		"oauth token": {map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken + "\n")}, testOAuthToken, "oauth-token"},
		"api key":     {map[string][]byte{ClaudeAPIKeyKey: []byte(testAPIKey)}, testAPIKey, "api-key"},
	} {
		t.Run(name, func(t *testing.T) {
			kube := kubefake.NewSimpleClientset(claudeSecret(tc.data))
			addon := newRunnerAddon(t, kube, tempHome(t))
			ctx := context.Background()
			t.Cleanup(func() { _ = addon.Stop(ctx) })

			if _, err := addon.Reconcile(ctx, claudeRunnerSpec()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			raw, err := os.ReadFile(addon.claudeCredentialPath())
			if err != nil {
				t.Fatalf("the credential was not materialized: %v", err)
			}
			if string(raw) != tc.want {
				t.Errorf("credential file = %q, want the Secret value verbatim", raw)
			}
			info, err := os.Stat(addon.claudeCredentialPath())
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0600 {
				t.Errorf("credential mode = %v, want 0600", perm)
			}
			if homeInfo, err := os.Stat(addon.claudeHome()); err != nil || homeInfo.Mode().Perm() != 0700 {
				t.Errorf("claude home = %v, %v; want a 0700 directory", homeInfo, err)
			}

			// The child is told where the credential is and how to inject it;
			// the value itself never appears on the command line.
			args := strings.Join(addon.childArgs(claudeRunnerSpec().Runner), " ")
			for _, want := range []string{
				"--harness claude",
				"--claude-credential-file " + addon.claudeCredentialPath(),
				"--claude-credential-kind " + tc.kind,
				"--claude-home " + addon.claudeHome(),
				"--claude-model sonnet",
				"--claude-permission-mode bypassPermissions",
				"--claude-allowed-tool Bash(git *)",
			} {
				if !strings.Contains(args, want) {
					t.Errorf("child args %q lack %q", args, want)
				}
			}
			if strings.Contains(args, tc.want) {
				t.Fatalf("the credential was passed on the command line: %s", args)
			}
			if strings.Contains(args, "--codex-home") {
				t.Errorf("the claude harness was given Codex arguments: %s", args)
			}
		})
	}
}

// TestClaudeAuthRefusals: an ambiguous or absent credential starts nothing and
// says which of the two problems it is.
func TestClaudeAuthRefusals(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		objects []func() *corev1.Secret
		spec    func() Spec
		reason  string
	}{
		"no authSecretRef": {
			spec: func() Spec {
				s := claudeRunnerSpec()
				s.Runner.Claude.AuthSecretRef = nil
				return s
			},
			reason: ReasonClaudeAuthMissing,
		},
		"missing Secret": {
			spec:   claudeRunnerSpec,
			reason: ReasonClaudeAuthMissing,
		},
		"neither key": {
			objects: []func() *corev1.Secret{func() *corev1.Secret {
				return claudeSecret(map[string][]byte{"token": []byte("wrong-key")})
			}},
			spec:   claudeRunnerSpec,
			reason: ReasonClaudeAuthMissing,
		},
		"both keys": {
			objects: []func() *corev1.Secret{func() *corev1.Secret {
				return claudeSecret(map[string][]byte{
					ClaudeOAuthTokenKey: []byte(testOAuthToken),
					ClaudeAPIKeyKey:     []byte(testAPIKey),
				})
			}},
			spec:   claudeRunnerSpec,
			reason: ReasonClaudeAuthInvalid,
		},
		"embedded newline": {
			objects: []func() *corev1.Secret{func() *corev1.Secret {
				return claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte("first\nsecond")})
			}},
			spec:   claudeRunnerSpec,
			reason: ReasonClaudeAuthInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var kube kubernetes.Interface = kubefake.NewSimpleClientset()
			if len(tc.objects) > 0 {
				kube = kubefake.NewSimpleClientset(tc.objects[0]())
			}
			addon := newRunnerAddon(t, kube, tempHome(t))
			t.Cleanup(func() { _ = addon.Stop(ctx) })

			status, err := addon.Reconcile(ctx, tc.spec())
			if err != nil {
				t.Fatalf("Reconcile returned an error instead of a condition: %v", err)
			}
			configured := conditionByType(status, ConditionConfigured)
			if configured == nil || configured.Status != metav1.ConditionFalse {
				t.Fatalf("Configured = %+v, want False", configured)
			}
			if configured.Reason != tc.reason {
				t.Errorf("Configured reason = %q, want %q", configured.Reason, tc.reason)
			}
			if addon.sup != nil {
				t.Error("a process was started without a usable credential")
			}
			if _, err := os.Stat(addon.claudeCredentialPath()); err == nil {
				t.Error("a credential file was written despite the refusal")
			}
			if _, err := kube.CoreV1().Secrets(TokenSecretNamespace).Get(ctx, "code"+TokenSecretSuffix, metav1.GetOptions{}); err == nil {
				t.Error("the token Secret was published without a usable credential")
			}
			// The refusal message must describe the problem, never the value.
			if strings.Contains(status.Message, testOAuthToken) || strings.Contains(status.Message, testAPIKey) {
				t.Errorf("the status message leaked a credential: %q", status.Message)
			}
		})
	}
}

// TestSwitchingHarnessRemovesTheOtherCredential: an Addon that moves between
// harnesses must not leave the previous harness's credential on the host.
func TestSwitchingHarnessRemovesTheOtherCredential(t *testing.T) {
	kube := kubefake.NewSimpleClientset(
		codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"x"}`)}),
		claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)}),
	)
	addon := newRunnerAddon(t, kube, tempHome(t))
	ctx := context.Background()
	t.Cleanup(func() { _ = addon.Stop(ctx) })

	// Start on Codex.
	if _, err := addon.Reconcile(ctx, runnerSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(addon.codexAuthPath()); err != nil {
		t.Fatalf("the Codex session was not materialized: %v", err)
	}
	codexHashBefore := addon.hash

	// Switch to Claude Code.
	if _, err := addon.Reconcile(ctx, claudeRunnerSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(addon.claudeCredentialPath()); err != nil {
		t.Fatalf("the Claude credential was not materialized: %v", err)
	}
	if _, err := os.Stat(addon.codexAuthPath()); err == nil {
		t.Error("the stale Codex session was left on disk after switching harness")
	}
	if addon.hash == codexHashBefore {
		t.Error("switching harness did not change the restart hash; the child would keep running the old harness")
	}
	// Homes are kept: they hold session state and losing them breaks resume.
	for _, home := range []string{addon.codexHome(), addon.claudeHome()} {
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			t.Errorf("home %s was removed: %v", home, err)
		}
	}

	// And back again.
	if _, err := addon.Reconcile(ctx, runnerSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(addon.claudeCredentialPath()); err == nil {
		t.Error("the stale Claude credential was left on disk after switching back")
	}
}

// TestCredentialRotationRestartsTheChildOnlyWhenContentChanges: the supervisor
// restart hash covers the credential, so a rotated Secret takes effect on the
// next resync — and an unchanged one does not bounce a working runner.
func TestCredentialRotationRestartsTheChildOnlyWhenContentChanges(t *testing.T) {
	ctx := context.Background()

	for name, tc := range map[string]struct {
		initial  *corev1.Secret
		rotate   func() *corev1.Secret
		spec     func() Spec
		wantSame bool
	}{
		"codex unchanged": {
			initial: codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"a"}`)}),
			rotate: func() *corev1.Secret {
				return codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"a"}`)})
			},
			spec:     runnerSpec,
			wantSame: true,
		},
		"codex rotated": {
			initial: codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"a"}`)}),
			rotate: func() *corev1.Secret {
				return codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"b"}`)})
			},
			spec:     runnerSpec,
			wantSame: false,
		},
		"claude unchanged": {
			initial: claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)}),
			rotate: func() *corev1.Secret {
				return claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)})
			},
			spec:     claudeRunnerSpec,
			wantSame: true,
		},
		"claude rotated": {
			initial: claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)}),
			rotate: func() *corev1.Secret {
				return claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte("sk-ant-oat01-rotated")})
			},
			spec:     claudeRunnerSpec,
			wantSame: false,
		},
		"claude swapped to an api key": {
			initial:  claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)}),
			rotate:   func() *corev1.Secret { return claudeSecret(map[string][]byte{ClaudeAPIKeyKey: []byte(testOAuthToken)}) },
			spec:     claudeRunnerSpec,
			wantSame: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			kube := kubefake.NewSimpleClientset(tc.initial)
			addon := newRunnerAddon(t, kube, tempHome(t))
			t.Cleanup(func() { _ = addon.Stop(ctx) })

			if _, err := addon.Reconcile(ctx, tc.spec()); err != nil {
				t.Fatal(err)
			}
			before := addon.hash
			if before == "" {
				t.Fatal("no restart hash was recorded")
			}

			rotated := tc.rotate()
			if _, err := kube.CoreV1().Secrets(rotated.Namespace).Update(ctx, rotated, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := addon.Reconcile(ctx, tc.spec()); err != nil {
				t.Fatal(err)
			}
			after := addon.hash

			if tc.wantSame && before != after {
				t.Error("an unchanged credential restarted the runner")
			}
			if !tc.wantSame && before == after {
				t.Error("a rotated credential did not restart the runner; it would keep using the old one")
			}
		})
	}
}

// TestHarnessStatusIsCopiedFromTheCapabilitiesProbe: a portal should be able to
// show "claude-code 2.1.273, ready" from the Addon alone, without holding a
// runner bearer token.
func TestHarnessStatusIsCopiedFromTheCapabilitiesProbe(t *testing.T) {
	kube := kubefake.NewSimpleClientset(claudeSecret(map[string][]byte{ClaudeOAuthTokenKey: []byte(testOAuthToken)}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocolVersion": "runner/v1",
			"runnerID":        "build-01-code",
			"version":         "v0.2.5",
			"harnesses": []map[string]any{{
				"name":    "claude-code",
				"version": "2.1.273",
				"ready":   false,
				"reasons": []string{"Claude Code authentication is not configured"},
			}},
		})
	}))
	defer server.Close()

	// The probe dials 127.0.0.1:<spec port>; point the add-on's HTTP client at
	// the fake capabilities server instead of standing up a real runner.
	addon := newRunnerAddon(t, kube, tempHome(t))
	addon.opts.HTTPClient = redirectingClient(server.URL)
	ctx := context.Background()
	t.Cleanup(func() { _ = addon.Stop(ctx) })

	status, err := addon.Reconcile(ctx, claudeRunnerSpec())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if status.Phase != PhaseRunning {
		t.Fatalf("phase = %q, want Running (message %q)", status.Phase, status.Message)
	}
	if status.Version != "v0.2.5" {
		t.Errorf("status.version = %q, want the runner build", status.Version)
	}
	if status.Harness == nil {
		t.Fatal("no harness was reported")
	}
	if status.Harness.Name != "claude-code" || status.Harness.Version != "2.1.273" {
		t.Errorf("harness = %+v", status.Harness)
	}
	// A runner can answer the protocol while its harness is unready; the
	// distinction is exactly what this block exists to carry.
	if status.Harness.Ready {
		t.Error("harness readiness was not carried through")
	}
	if len(status.Harness.Reasons) != 1 || !strings.Contains(status.Harness.Reasons[0], "authentication") {
		t.Errorf("harness reasons = %v", status.Harness.Reasons)
	}
}
