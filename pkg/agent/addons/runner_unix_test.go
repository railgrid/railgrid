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
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/railgrid/railgrid/pkg/runner"
)

// stubExecutable writes a script that stands in for `railgrid runner run`: it
// accepts any arguments and stays up, so the supervisor has something real to
// manage without the tests depending on Codex or a listening runner.
func stubExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-agent")
	script := "#!/bin/sh\nexec sleep 300\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// tempHome resolves the temporary directory's symlinks. On macOS t.TempDir()
// lives under /var, which is a symlink to /private/var, and the add-on writer
// deliberately refuses to write through a symlinked path component. A real
// account's home is not behind a symlink, so resolving here tests the code
// rather than the platform's /var.
func tempHome(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func codexSecret(name, namespace string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
}

func newRunnerAddon(t *testing.T, kube kubernetes.Interface, home string) *runnerAddon {
	t.Helper()
	factory, err := NewRunnerFactory(RunnerOptions{
		EdgeName:   "build-01",
		Executable: stubExecutable(t),
		Account:    RunAsAccount{Home: home, UID: InheritUID, GID: InheritUID},
		ReadAuth: func(ctx context.Context, _ string, ref *SecretRef) (*corev1.Secret, error) {
			namespace := ref.Namespace
			if namespace == "" {
				namespace = "default"
			}
			return kube.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		},
		PublishToken: func(ctx context.Context, spec Spec, token string) error {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: spec.Name + TokenSecretSuffix, Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: AddonKind, Name: spec.Name, UID: spec.UID}}}, Data: map[string][]byte{TokenSecretKey: []byte(token)}}
			_, err := kube.CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				_, err = kube.CoreV1().Secrets("default").Update(ctx, secret, metav1.UpdateOptions{})
			}
			return err
		},
		ProbeDeadline:  50 * time.Millisecond,
		ProbeTimeout:   50 * time.Millisecond,
		InitialBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	addon, err := factory("code")
	if err != nil {
		t.Fatal(err)
	}
	return addon.(*runnerAddon)
}

func runnerSpec() Spec {
	return Spec{
		Name:       "code",
		UID:        "addon-uid-1",
		Generation: 3,
		EdgeRef:    EdgeRef{Kind: "LinuxServer", Name: "build-01"},
		Type:       TypeRunner,
		Runner: &RunnerSpec{
			Port:                     8787,
			MaximumCapacity:          1,
			Toolchains:               []string{"go1.26"},
			VerificationCapabilities: []string{"unit"},
			Repositories: map[string]Repository{
				"app": {Source: "/srv/repos/app"},
			},
			Codex: &Codex{
				Binary:        "codex",
				AuthSecretRef: &SecretRef{Name: "codex-auth", Namespace: "default"},
			},
		},
	}
}

// TestRunnerAddonMaterializesOwnerOnlyState: a successful reconcile leaves the
// add-on's whole state tree owner-only. These modes are the only thing standing
// between another local account and the runner's bearer token or the tenant's
// Codex session.
func TestRunnerAddonMaterializesOwnerOnlyState(t *testing.T) {
	kube := kubefake.NewSimpleClientset(codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte(`{"session":"x"}`)}))
	home := tempHome(t)
	addon := newRunnerAddon(t, kube, home)
	ctx := context.Background()
	t.Cleanup(func() { _ = addon.Stop(ctx) })

	status, err := addon.Reconcile(ctx, runnerSpec())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Nothing is listening on the loopback port, so Running is False; the point
	// here is that everything else was materialized.
	if status.Phase != PhaseInstalling && status.Phase != PhaseDegraded {
		t.Fatalf("phase = %q, want Installing or Degraded without a live runner", status.Phase)
	}

	for path, want := range map[string]os.FileMode{
		addon.stateDir():      0700,
		addon.codexHome():     0700,
		addon.runnerState():   0700,
		addon.tokenPath():     0600,
		addon.configPath():    0600,
		addon.codexAuthPath(): 0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != want {
			t.Errorf("%s mode = %v, want %v", path, perm, want)
		}
	}

	// The state directory is under the run-as account's home, and nowhere else.
	if want := filepath.Join(home, ".railgrid", "addons", "runner", "code"); addon.stateDir() != want {
		t.Errorf("stateDir = %s, want %s", addon.stateDir(), want)
	}
}

// TestRunnerTokenIsStableAcrossReconciles: the published Secret is what the
// provider's Service authenticates with. Regenerating the token on every
// reconcile would break every in-flight attempt for no reason.
func TestRunnerTokenIsStableAcrossReconciles(t *testing.T) {
	kube := kubefake.NewSimpleClientset(codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte("{}")}))
	addon := newRunnerAddon(t, kube, tempHome(t))
	ctx := context.Background()
	t.Cleanup(func() { _ = addon.Stop(ctx) })

	if _, err := addon.Reconcile(ctx, runnerSpec()); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(addon.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 2*tokenBytes {
		t.Fatalf("token is only %d bytes", len(first))
	}
	if _, err := addon.Reconcile(ctx, runnerSpec()); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(addon.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("the runner token was rotated on a re-reconcile")
	}

	secret, err := kube.CoreV1().Secrets(TokenSecretNamespace).Get(ctx, "code"+TokenSecretSuffix, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("token Secret was not published: %v", err)
	}
	if got := string(secret.Data[TokenSecretKey]); got != string(trimNewline(second)) {
		t.Errorf("published token %q does not match the on-disk token", got)
	}
	if len(secret.OwnerReferences) != 1 {
		t.Fatalf("token Secret has %d owner references, want 1", len(secret.OwnerReferences))
	}
	owner := secret.OwnerReferences[0]
	if owner.Kind != AddonKind || owner.Name != "code" || string(owner.UID) != "addon-uid-1" {
		t.Errorf("token Secret owner = %+v, want the Addon", owner)
	}
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// TestRunnerAddonRefusesASymlinkedStateDirectory: the add-on account owns its
// own home, so it could point ~/.railgrid at somewhere else and have the agent
// (possibly root) write there.
func TestRunnerAddonRefusesASymlinkedStateDirectory(t *testing.T) {
	kube := kubefake.NewSimpleClientset(codexSecret("codex-auth", "default", map[string][]byte{CodexAuthKey: []byte("{}")}))
	home := tempHome(t)
	elsewhere := tempHome(t)
	if err := os.Symlink(elsewhere, filepath.Join(home, ".railgrid")); err != nil {
		t.Fatal(err)
	}
	addon := newRunnerAddon(t, kube, home)

	if _, err := addon.Reconcile(context.Background(), runnerSpec()); err == nil {
		t.Fatal("a symlinked ~/.railgrid was accepted")
	}
	if entries, err := os.ReadDir(elsewhere); err == nil && len(entries) > 0 {
		t.Errorf("the agent wrote through the symlink: %v", entries)
	}
}

// TestRunnerAddonWithoutCodexAuthStartsNothing: a runner with no Codex session
// would accept attempts and fail every one of them, so it is reported rather
// than started — and no token Secret is published, which keeps the provider
// from publishing a Service for it.
func TestRunnerAddonWithoutCodexAuthStartsNothing(t *testing.T) {
	ctx := context.Background()

	for name, spec := range map[string]func() Spec{
		"no authSecretRef": func() Spec {
			s := runnerSpec()
			s.Runner.Codex.AuthSecretRef = nil
			return s
		},
		"missing Secret": func() Spec { return runnerSpec() },
		"Secret without the auth.json key": func() Spec {
			return runnerSpec()
		},
	} {
		t.Run(name, func(t *testing.T) {
			var kube kubernetes.Interface = kubefake.NewSimpleClientset()
			if name == "Secret without the auth.json key" {
				kube = kubefake.NewSimpleClientset(codexSecret("codex-auth", "default", map[string][]byte{"other": []byte("x")}))
			}
			addon := newRunnerAddon(t, kube, tempHome(t))
			t.Cleanup(func() { _ = addon.Stop(ctx) })

			status, err := addon.Reconcile(ctx, spec())
			if err != nil {
				t.Fatalf("Reconcile returned an error instead of a condition: %v", err)
			}
			configured := conditionByType(status, ConditionConfigured)
			if configured == nil || configured.Status != metav1.ConditionFalse {
				t.Fatalf("Configured = %+v, want False", configured)
			}
			if configured.Reason != ReasonCodexAuthMissing {
				t.Errorf("Configured reason = %q, want %q", configured.Reason, ReasonCodexAuthMissing)
			}
			if addon.sup != nil {
				t.Error("a process was started without a Codex session")
			}
			if _, err := os.Stat(addon.configPath()); err == nil {
				t.Error("runner.json was written without a Codex session")
			}
			if _, err := kube.CoreV1().Secrets(TokenSecretNamespace).Get(ctx, "code"+TokenSecretSuffix, metav1.GetOptions{}); err == nil {
				t.Error("the token Secret was published without a Codex session")
			}
		})
	}
}

// TestRenderedConfigRoundTripsThroughLoadConfig: the agent renders the same
// JSON an operator would hand-write, so it has to survive the runner's own
// loader unchanged.
func TestRenderedConfigRoundTripsThroughLoadConfig(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	addon := newRunnerAddon(t, kube, tempHome(t))
	spec := runnerSpec()

	data, err := addon.renderConfig(spec.Runner)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := runner.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ProtocolVersion != runner.ProtocolVersion {
		t.Errorf("protocolVersion = %q", cfg.ProtocolVersion)
	}
	if cfg.RunnerID != "build-01-code" {
		t.Errorf("runnerID = %q, want build-01-code", cfg.RunnerID)
	}
	if cfg.Listen != "127.0.0.1:8787" {
		t.Errorf("listen = %q, want the loopback address", cfg.Listen)
	}
	if cfg.TokenFile != addon.tokenPath() || cfg.StateDir != addon.runnerState() {
		t.Errorf("paths = %q / %q", cfg.TokenFile, cfg.StateDir)
	}
	if cfg.MaximumCapacity != 1 {
		t.Errorf("maximumCapacity = %d, want 1", cfg.MaximumCapacity)
	}
	if repo, ok := cfg.Repositories["app"]; !ok || repo.Source != "/srv/repos/app" {
		t.Errorf("repositories = %+v", cfg.Repositories)
	}
	// The token never travels in the configuration file; it is read from
	// tokenFile so it cannot leak through a config backup.
	if cfg.Token != "" {
		t.Error("the bearer token was serialized into runner.json")
	}
}

// TestRenderConfigRejectsUnsafeEnrollment: the runner would refuse these at
// load time and crash-loop; refusing here turns them into a readable condition.
func TestRenderConfigRejectsUnsafeEnrollment(t *testing.T) {
	addon := newRunnerAddon(t, kubefake.NewSimpleClientset(), tempHome(t))

	relative := runnerSpec()
	relative.Runner.Repositories = map[string]Repository{"app": {Source: "relative/path"}}
	if _, err := addon.renderConfig(relative.Runner); err == nil {
		t.Error("a relative repository source was accepted")
	}

	badID := runnerSpec()
	badID.Runner.Repositories = map[string]Repository{"bad id": {Source: "/srv/app"}}
	if _, err := addon.renderConfig(badID.Runner); err == nil {
		t.Error("an invalid repository ID was accepted")
	}

	capacity := runnerSpec()
	capacity.Runner.MaximumCapacity = 4
	if _, err := addon.renderConfig(capacity.Runner); err == nil {
		t.Error("maximumCapacity 4 was accepted by the single-execution runner")
	}
}

func conditionByType(status Status, condType string) *Condition {
	for i := range status.Conditions {
		if status.Conditions[i].Type == condType {
			return &status.Conditions[i]
		}
	}
	return nil
}
