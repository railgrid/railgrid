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
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	cliauth "github.com/railgrid/railgrid/pkg/cli/auth"
)

// noAutoLoginEnv turns the automatic re-login off; the exec plugin then fails
// with the 'railgrid login' hint as it does without a terminal.
const noAutoLoginEnv = "RAILGRID_NO_AUTO_LOGIN"

// reloginTimeout bounds how long a command waits for the browser sign-in.
const reloginTimeout = 5 * time.Minute

// Seams for tests.
var (
	// canAutoLogin reports whether a person is there to complete a browser
	// sign-in. The exec plugin's stdout is the credential and its stdin is
	// usually not passed through, so stderr is the one stream that says
	// whether the command was started from a terminal.
	canAutoLogin = func() bool {
		if v := strings.TrimSpace(os.Getenv(noAutoLoginEnv)); v != "" && v != "0" && v != "false" {
			return false
		}
		return term.IsTerminal(int(os.Stderr.Fd()))
	}
	loginInBrowser                 = browserLogin
	reloginMessages      io.Writer = os.Stderr
	hubForExecCredential           = hubFromKubeconfig
)

// reloginAndGetToken recovers from a dead login by running the browser flow
// against the hub the kubeconfig names for these credentials, then returns
// the fresh token. cause is returned unchanged when a re-login is not
// possible here (no terminal, opted out, hub unknown), so scripts and CI keep
// the plain 'railgrid login' hint.
func reloginAndGetToken(ctx context.Context, cause error, issuerURL, clientID string, insecure bool, account string) (string, int64, error) {
	if !canAutoLogin() {
		return "", 0, cause
	}
	hubURL, ok := hubForExecCredential(issuerURL, clientID, account)
	if !ok {
		return "", 0, cause
	}

	unlock, err := cliauth.LockLogin(issuerURL, clientID, account)
	if err != nil {
		return "", 0, cause
	}
	defer unlock()

	// Another command may have signed in while this one waited for the lock.
	if token, expiresAt, err := cachedOrRefreshedToken(ctx, issuerURL, clientID, insecure, account); err == nil {
		return token, expiresAt, nil
	}

	_, _ = fmt.Fprintf(reloginMessages, "Your railgrid login for %s has expired; signing in again.\n", hubURL)
	ctx, cancel := context.WithTimeout(ctx, reloginTimeout)
	defer cancel()
	resp, err := loginInBrowser(ctx, reloginMessages, hubURL)
	if err != nil {
		return "", 0, fmt.Errorf("%w; automatic re-login failed: %w", cause, err)
	}
	if resp.IDToken == "" || resp.IssuerURL != issuerURL || resp.ClientID != clientID {
		return "", 0, fmt.Errorf("%w; the hub at %s no longer issues tokens for this kubeconfig entry, run 'railgrid login --hub-url %s'", cause, hubURL, hubURL)
	}
	if account != "" && resp.UserID != account {
		return "", 0, fmt.Errorf("signed in as %s, but this kubeconfig entry belongs to account %s; run 'railgrid login --hub-url %s' to switch accounts", loginName(resp), account, hubURL)
	}
	// The command that needed the token may be running under any context, so
	// the current one is left alone.
	if _, _, err := storeOIDCLogin(resp, true); err != nil {
		return "", 0, err
	}
	_, _ = fmt.Fprintf(reloginMessages, "Logged in as %s.\n", loginName(resp))
	return resp.IDToken, resp.ExpiresAt, nil
}

func loginName(resp tenancyv1alpha1.LoginResponse) string {
	if resp.Email != "" {
		return resp.Email
	}
	return resp.UserID
}

// hubFromKubeconfig finds the hub that a get-token invocation authenticates
// to: the server of a context whose user runs this plugin with the same
// issuer, client and account. The "railgrid" context wins when several match.
func hubFromKubeconfig(issuerURL, clientID, account string) (string, bool) {
	raw, _, err := loadRawKubeconfig()
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == railgridContextName) != (names[j] == railgridContextName) {
			return names[i] == railgridContextName
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		kctx := raw.Contexts[name]
		auth, cluster := raw.AuthInfos[kctx.AuthInfo], raw.Clusters[kctx.Cluster]
		if auth == nil || auth.Exec == nil || cluster == nil || cluster.Server == "" {
			continue
		}
		if i, c, a := execOIDCArgs(auth.Exec); i != issuerURL || c != clientID || a != account {
			continue
		}
		base, _ := apiurl.SplitBaseAndCluster(cluster.Server)
		return base, true
	}
	return "", false
}

// loggedInHub returns the hub of the kubeconfig's "railgrid" context, which is
// what 'railgrid login' targets when no hub is named. Only that context counts:
// the current context may be any cluster at all.
func loggedInHub() (string, *clientcmdapi.Cluster) {
	raw, _, err := loadRawKubeconfig()
	if err != nil {
		return "", nil
	}
	kctx := raw.Contexts[railgridContextName]
	if kctx == nil {
		return "", nil
	}
	cluster := raw.Clusters[kctx.Cluster]
	if cluster == nil || cluster.Server == "" {
		return "", nil
	}
	base, _ := apiurl.SplitBaseAndCluster(cluster.Server)
	return base, cluster
}
