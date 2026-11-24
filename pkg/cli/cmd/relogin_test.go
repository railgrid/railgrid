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

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	cliauth "github.com/railgrid/railgrid/pkg/cli/auth"
)

const reloginHub = "https://hub.example.com"

// oidcKubeconfig is a kubeconfig as 'railgrid login' leaves it: the railgrid
// context on a selected workspace, its user running the get-token plugin.
func oidcKubeconfig(issuer, clientID, account, workspace string) *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["railgrid"] = &clientcmdapi.Cluster{Server: reloginHub + "/clusters/" + workspace}
	cfg.AuthInfos["railgrid"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion: "client.authentication.k8s.io/v1beta1",
		Command:    "railgrid",
		Args:       []string{"get-token", "--oidc-issuer-url=" + issuer, "--oidc-client-id=" + clientID, "--oidc-account=" + account},
	}}
	cfg.Contexts["railgrid"] = &clientcmdapi.Context{Cluster: "railgrid", AuthInfo: "railgrid"}
	cfg.CurrentContext = "railgrid"
	return cfg
}

// reloginFixture seeds an expired login whose refresh token the issuer
// rejects, a kubeconfig whose current context is some other cluster, and a
// browser flow that succeeds as loginAs. It returns the kubeconfig path and
// the number of browser logins.
func reloginFixture(t *testing.T, iss *fakeIssuer, interactive bool, loginAs string) (string, *int) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	iss.reject.Store(true)
	if err := cliauth.SaveTokenCache(&cliauth.TokenCache{
		IDToken:      "id-stale",
		RefreshToken: "rt-dead",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
		IssuerURL:    iss.URL,
		ClientID:     iss.clientID,
		Account:      "user-abc",
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := oidcKubeconfig(iss.URL, iss.clientID, "user-abc", "selected")
	cfg.Clusters["other"] = &clientcmdapi.Cluster{Server: "https://other.example.com"}
	cfg.AuthInfos["other"] = &clientcmdapi.AuthInfo{Token: "x"}
	cfg.Contexts["other"] = &clientcmdapi.Context{Cluster: "other", AuthInfo: "other"}
	cfg.CurrentContext = "other"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	setLoginKubeconfig(t, path)

	logins := 0
	prevCan, prevLogin, prevMsg := canAutoLogin, loginInBrowser, reloginMessages
	t.Cleanup(func() { canAutoLogin, loginInBrowser, reloginMessages = prevCan, prevLogin, prevMsg })
	canAutoLogin = func() bool { return interactive }
	reloginMessages = io.Discard
	loginInBrowser = func(_ context.Context, _ io.Writer, hubURL string) (tenancyv1alpha1.LoginResponse, error) {
		logins++
		if hubURL != reloginHub {
			return tenancyv1alpha1.LoginResponse{}, errors.New("unexpected hub " + hubURL)
		}
		home, err := clientcmd.Write(*oidcKubeconfig(iss.URL, iss.clientID, "", "home"))
		if err != nil {
			return tenancyv1alpha1.LoginResponse{}, err
		}
		return tenancyv1alpha1.LoginResponse{
			Kubeconfig:   home,
			IDToken:      "id-fresh",
			RefreshToken: "rt-fresh",
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
			IssuerURL:    iss.URL,
			ClientID:     iss.clientID,
			UserID:       loginAs,
			Email:        loginAs + "@example.com",
		}, nil
	}
	return path, &logins
}

// TestGetTokenReloginsWhenRefreshTokenIsDead: from a terminal, a rejected
// refresh token leads to one browser login against the hub in the kubeconfig
// and the command gets its token; the user's current context and selected
// workspace survive.
func TestGetTokenReloginsWhenRefreshTokenIsDead(t *testing.T) {
	iss := newFakeIssuer(t)
	path, logins := reloginFixture(t, iss, true, "user-abc")

	var out bytes.Buffer
	if err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false, "user-abc"); err != nil {
		t.Fatalf("runGetToken: %v", err)
	}
	if got := decodeExecCredential(t, out.Bytes()).Status.Token; got != "id-fresh" {
		t.Fatalf("token = %q, want the one from the re-login", got)
	}
	if *logins != 1 {
		t.Fatalf("browser logins = %d, want 1", *logins)
	}
	cache, err := cliauth.LoadTokenCache(iss.URL, iss.clientID, "user-abc")
	if err != nil || cache.RefreshToken != "rt-fresh" {
		t.Fatalf("cache = %+v, %v; want the new refresh token stored", cache, err)
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "other" {
		t.Errorf("current-context = %q, want it left on %q", cfg.CurrentContext, "other")
	}
	if got := cfg.Clusters["railgrid"].Server; got != reloginHub+"/clusters/selected" {
		t.Errorf("railgrid server = %q, want the selected workspace kept", got)
	}

	// The next call is served from the cache: no second browser window.
	out.Reset()
	if err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false, "user-abc"); err != nil {
		t.Fatalf("second runGetToken: %v", err)
	}
	if *logins != 1 {
		t.Fatalf("browser logins after cached read = %d, want still 1", *logins)
	}
}

// TestGetTokenNoReloginWithoutTerminal: scripts and CI keep the plain error.
func TestGetTokenNoReloginWithoutTerminal(t *testing.T) {
	iss := newFakeIssuer(t)
	_, logins := reloginFixture(t, iss, false, "user-abc")

	var out bytes.Buffer
	err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false, "user-abc")
	if err == nil || !strings.Contains(err.Error(), "run 'railgrid login'") {
		t.Fatalf("err = %v, want the re-login hint", err)
	}
	if *logins != 0 || out.Len() != 0 {
		t.Fatalf("logins = %d, stdout = %q; want neither", *logins, out.String())
	}
}

// TestGetTokenNoReloginWhenIssuerUnreachable: only a rejected refresh token
// means the login is dead; an outage must not open a browser.
func TestGetTokenNoReloginWhenIssuerUnreachable(t *testing.T) {
	iss := newFakeIssuer(t)
	_, logins := reloginFixture(t, iss, true, "user-abc")
	if refreshTokenRejected(errors.New("dial tcp: connection refused")) {
		t.Fatal("a transport error was classified as a rejected refresh token")
	}
	iss.Close()

	err := runGetToken(context.Background(), &bytes.Buffer{}, iss.URL, iss.clientID, false, "user-abc")
	if err == nil {
		t.Fatal("want an error while the issuer is down")
	}
	if *logins != 0 {
		t.Fatalf("browser logins = %d, want 0", *logins)
	}
}

// TestGetTokenReloginAsAnotherAccount: signing in as someone else must not
// hand that person's token to a kubeconfig entry of the original account.
func TestGetTokenReloginAsAnotherAccount(t *testing.T) {
	iss := newFakeIssuer(t)
	path, _ := reloginFixture(t, iss, true, "user-other")
	before, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = runGetToken(context.Background(), &out, iss.URL, iss.clientID, false, "user-abc")
	if err == nil || !strings.Contains(err.Error(), "belongs to account user-abc") {
		t.Fatalf("err = %v, want an account mismatch", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want no credential", out.String())
	}
	after, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := after.AuthInfos["railgrid"].Exec.Args, before.AuthInfos["railgrid"].Exec.Args; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("kubeconfig user rewritten to %v", got)
	}
}

// TestLoginDefaultsToTheHubInKubeconfig: 'railgrid login' with no hub named
// reuses the hub of the existing session instead of failing.
func TestLoginDefaultsToTheHubInKubeconfig(t *testing.T) {
	t.Setenv(hubURLEnv, "")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*oidcKubeconfig("https://issuer", "client", "user-abc", "selected"), path); err != nil {
		t.Fatal(err)
	}
	setLoginKubeconfig(t, path)
	if hub, cluster := loggedInHub(); hub != reloginHub || cluster == nil {
		t.Fatalf("loggedInHub = %q, want %q", hub, reloginHub)
	}

	// A kubeconfig without a railgrid context offers no default, whatever its
	// current context points at.
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["other"] = &clientcmdapi.Cluster{Server: "https://other.example.com"}
	cfg.Contexts["other"] = &clientcmdapi.Context{Cluster: "other"}
	cfg.CurrentContext = "other"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	if hub, _ := loggedInHub(); hub != "" {
		t.Fatalf("loggedInHub = %q, want none without a railgrid context", hub)
	}
}
