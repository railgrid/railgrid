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

package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/connsecret"
)

// connectionsCR is a CR reader that knows connections, which fakeCR does not.
type connectionsCR struct {
	fakeCR
	connections map[string]*agentsv1alpha1.Connection
}

func (c connectionsCR) GetConnection(_ context.Context, name string) (*agentsv1alpha1.Connection, error) {
	if conn, ok := c.connections[name]; ok {
		return conn, nil
	}
	return nil, fmt.Errorf("connection %q not found", name)
}

func githubConnection(name, secretRef string) *agentsv1alpha1.Connection {
	conn := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}}
	conn.Spec.Type = agentsv1alpha1.ConnectionTypeGitHub
	conn.Spec.SecretRef = secretRef
	return conn
}

func tokenSecret(name, token string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string][]byte{"token": []byte(token)}}
}

// The harness runs with the token of the connection the agent names, read from
// the connection's OWN Secret and nothing else: a connection whose secretRef
// points elsewhere is refused, because Connection write must not turn into a
// read of any token-bearing Secret in the namespace.
func TestHarnessEnvironmentReadsOnlyTheConnectionsOwnToken(t *testing.T) {
	slack := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "team-slack"}}
	slack.Spec.Type = agentsv1alpha1.ConnectionTypeSlack
	cr := connectionsCR{connections: map[string]*agentsv1alpha1.Connection{
		"gh":         githubConnection("gh", ""),
		"gh-own":     githubConnection("gh-own", connsecret.Name("gh-own")),
		"gh-custom":  githubConnection("gh-custom", "somebody-elses-secret"),
		"gh-empty":   githubConnection("gh-empty", ""),
		"team-slack": slack,
	}}
	creds := fakeCreds{secrets: map[string]*corev1.Secret{
		connsecret.Name("gh"):       tokenSecret(connsecret.Name("gh"), "ghp_reviewer"),
		connsecret.Name("gh-own"):   tokenSecret(connsecret.Name("gh-own"), "ghp_own"),
		"somebody-elses-secret":     tokenSecret("somebody-elses-secret", "ghp_not_yours"),
		connsecret.Name("gh-empty"): {ObjectMeta: metav1.ObjectMeta{Name: connsecret.Name("gh-empty")}, Data: map[string][]byte{"other": []byte("x")}},
	}}
	run := taskRun{Creds: creds, CR: cr}
	s := &Server{}

	for _, test := range []struct {
		name    string
		ref     string
		want    string
		wantErr string
	}{
		{"no connection named", "", "", ""},
		{"conventional secret", "gh", "ghp_reviewer", ""},
		{"secretRef naming its own secret", "gh-own", "ghp_own", ""},
		{"custom secretRef", "gh-custom", "", "custom secretRef"},
		{"no token key", "gh-empty", "", "holds no token"},
		{"not a github connection", "team-slack", "", "must be a github connection"},
		{"unknown connection", "missing", "", "not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, err := s.harnessEnvironment(context.Background(), run, &agentsv1alpha1.AgentHarnessBackend{GitHubConnectionRef: test.ref})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, test.wantErr)
				}
				if strings.Contains(fmt.Sprint(err), "ghp_") {
					t.Fatalf("the error carries a token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("harnessEnvironment: %v", err)
			}
			if test.want == "" {
				if env != nil {
					t.Fatalf("environment = %+v, want none", env)
				}
				return
			}
			if len(env) != 2 || env[0].Name != "GH_TOKEN" || env[0].Value != test.want || env[1].Name != "GITHUB_TOKEN" || env[1].Value != test.want {
				t.Fatalf("environment = %+v, want GH_TOKEN and GITHUB_TOKEN = %q", env, test.want)
			}
		})
	}
}
