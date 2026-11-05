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

package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Remote MCP servers, Codex side.
//
// The operator's config.toml may not name an MCP server, and that stays true:
// a server there would be one every launch reaches, whoever the identity is.
// A server the identity brings is instead attached to that launch's
// app-server alone, with root-level `-c key=value` overrides:
//
//	-c mcp_servers.<name>.url="<url>"
//	-c mcp_servers.<name>.bearer_token_env_var="RAILGRID_MCP_TOKEN_<NAME>"
//
// The bearer itself is only ever in the child's environment, under the name
// the override points at, because argv is readable by every process on the
// machine. Codex's default shell environment policy drops variables whose
// names contain TOKEN from the commands the model runs, and the operator
// config cannot change that policy, so the bearer reaches Codex's MCP client
// and not the model's shell.
//
// Probe starts the same app-server and must not reach anybody's server, which
// is why the overrides are added on the Run path only.

// mcpTokenEnvPrefix names the variable a server's bearer travels in.
const mcpTokenEnvPrefix = "RAILGRID_MCP_TOKEN_"

// mcpServerName is the runner's name rule, repeated so an adapter reached by
// a runner that forgot it still cannot be handed a key that changes the shape
// of a -c override or an environment variable name.
var mcpServerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// mcpTokenEnv is the environment variable that carries one server's bearer.
func mcpTokenEnv(name string) string {
	return mcpTokenEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// mcpOverrides returns the -c arguments and the environment entries that
// attach the launch's remote servers. Two names that differ only in "-"
// against "_" cannot collide, because "_" is not a legal server name.
func mcpOverrides(servers []harness.MCPServer) (args, env []string, err error) {
	seen := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		if !mcpServerName.MatchString(server.Name) {
			return nil, nil, fmt.Errorf("remote MCP server name %q is not a valid server name", server.Name)
		}
		if _, dup := seen[server.Name]; dup {
			return nil, nil, fmt.Errorf("remote MCP server %q is named twice", server.Name)
		}
		seen[server.Name] = struct{}{}
		if server.URL == "" || server.Token == "" || strings.ContainsAny(server.Token, "\r\n\x00") {
			return nil, nil, fmt.Errorf("remote MCP server %q has no usable url or token", server.Name)
		}
		// A JSON string is a valid TOML basic string, which is how Codex
		// parses an override value; quoting it this way means no URL can end
		// the value early and become a second key.
		url, err := json.Marshal(server.URL)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding remote MCP server %q url: %w", server.Name, err)
		}
		variable := mcpTokenEnv(server.Name)
		args = append(args,
			"-c", "mcp_servers."+server.Name+".url="+string(url),
			"-c", "mcp_servers."+server.Name+`.bearer_token_env_var="`+variable+`"`,
		)
		env = append(env, variable+"="+server.Token)
	}
	return args, env, nil
}

// mcpSecrets is every rendering of the launch's bearers that redaction must
// catch: the header value first, so it collapses to one marker, then the bare
// token.
func mcpSecrets(servers []harness.MCPServer) []string {
	out := make([]string, 0, 2*len(servers))
	for _, server := range servers {
		if server.Token != "" {
			out = append(out, "Bearer "+server.Token, server.Token)
		}
	}
	return out
}

func redactSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

// redactingEmit scrubs the bearers from everything an event carries. The
// app-server is not expected to echo one, but a failing MCP client's error,
// or the model reading its own environment, can, and an event is durable.
// The replacement is textual on the raw JSON: a token holds no character JSON
// escapes (the runner refuses whitespace and control characters), so it
// appears verbatim inside a string and the result stays valid JSON.
func redactingEmit(emit harness.Emit, secrets []string) harness.Emit {
	if emit == nil || len(secrets) == 0 {
		return emit
	}
	return func(event harness.Event) error {
		event.Message = redactSecrets(event.Message, secrets)
		if len(event.Data) > 0 {
			event.Data = json.RawMessage(redactSecrets(string(event.Data), secrets))
		}
		if event.Clarification != nil {
			clarification := *event.Clarification
			clarification.Text = redactSecrets(clarification.Text, secrets)
			event.Clarification = &clarification
		}
		return emit(event)
	}
}

// redactResult scrubs the bearers from what Run returns.
func redactResult(result harness.Result, err error, secrets []string) (harness.Result, error) {
	if len(secrets) == 0 {
		return result, err
	}
	result.Blocker = redactSecrets(result.Blocker, secrets)
	if result.Clarification != nil {
		clarification := *result.Clarification
		clarification.Text = redactSecrets(clarification.Text, secrets)
		result.Clarification = &clarification
	}
	if err != nil {
		if message := err.Error(); redactSecrets(message, secrets) != message {
			err = errors.New(redactSecrets(message, secrets))
		}
	}
	return result, err
}
