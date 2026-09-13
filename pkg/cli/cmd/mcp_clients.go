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

package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
)

// `faros mcp claude` / `faros mcp codex` register the workspace's aggregate
// MCPServer with a local AI client, then say how to start the client so it
// accepts the hub's TLS certificate. A hub with a publicly trusted certificate
// needs nothing; a local hub (faros dev init) presents a certificate from its
// own dev CA, which the client has to trust (--ca-file) or — Claude Code only —
// stop verifying.

// codexTokenEnvVar is the variable Codex reads the MCP bearer token from.
const codexTokenEnvVar = "FAROS_MCP_TOKEN"

type mcpClientOptions struct {
	mcpserverName string
	name          string
	caFile        string
	dryRun        bool
	scope         string // Claude Code only
}

func (o *mcpClientOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.mcpserverName, "mcpserver-name", "default", "Aggregate MCPServer to connect")
	cmd.Flags().StringVar(&o.name, "name", "", "Server name in the client's configuration (default: faros-<mcpserver-name>)")
	cmd.Flags().StringVar(&o.caFile, "ca-file", "", "PEM CA that signs the hub's certificate, for a hub whose certificate is not publicly trusted (faros dev init writes <cluster>-ca.crt)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the commands instead of running them")
}

func (o *mcpClientOptions) serverName() string {
	if o.name != "" {
		return o.name
	}
	return mcpServerName("", o.mcpserverName)
}

func newMCPClaudeCommand() *cobra.Command {
	o := &mcpClientOptions{}
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Add the workspace MCP server to Claude Code",
		Long: `Registers the workspace's aggregate MCP server with Claude Code
(claude mcp add --transport http), authenticated with the workspace's
long-lived MCP token. An existing entry with the same name is replaced.

When the hub's certificate is not publicly trusted, the command prints how
to start Claude Code so it accepts it: with --ca-file, by trusting that CA
(NODE_EXTRA_CA_CERTS); without it, by turning certificate verification off
(NODE_TLS_REJECT_UNAUTHORIZED=0) for that session.`,
		Example: `  # Publicly trusted hub
  faros mcp claude

  # Local hub from faros dev init
  faros mcp claude --ca-file faros-hub-ca.crt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCPClaude(cmd.OutOrStdout(), o)
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&o.scope, "scope", "user", "Claude Code configuration scope: user (every project), local (this project, private) or project (.mcp.json)")
	return cmd
}

func newMCPCodexCommand() *cobra.Command {
	o := &mcpClientOptions{}
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Add the workspace MCP server to Codex",
		Long: `Registers the workspace's aggregate MCP server with Codex
(codex mcp add --url), reading the workspace's long-lived MCP token from
` + codexTokenEnvVar + `. An existing entry with the same name is replaced.

Codex cannot skip certificate verification. For a hub whose certificate is
not publicly trusted, pass --ca-file: the command writes a bundle of your
system roots plus that CA and prints how to start Codex with it
(CODEX_CA_CERTIFICATE, Codex 0.129.0 or later).`,
		Example: `  # Publicly trusted hub
  faros mcp codex

  # Local hub from faros dev init
  faros mcp codex --ca-file faros-hub-ca.crt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCPCodex(cmd.OutOrStdout(), o)
		},
	}
	o.addFlags(cmd)
	return cmd
}

// mcpEndpoint is what a client needs: the aggregate endpoint and its token.
type mcpEndpoint struct {
	URL   string
	Token string
}

// resolveMCPEndpoint asks the hub for the aggregate MCPServer's endpoint and
// long-lived token for the workspace the current kubeconfig context targets.
func resolveMCPEndpoint(mcpserverName string) (*mcpEndpoint, error) {
	serverURL, err := currentServerURL()
	if err != nil {
		return nil, err
	}
	info, err := connectMCPForURL(serverURL, mcpserverName)
	if err != nil {
		return nil, fmt.Errorf("fetching MCP server %q from the hub (run 'faros login' and 'faros use' first): %w", mcpserverName, err)
	}
	if info.Token == "" {
		return nil, fmt.Errorf("the hub has not minted a token for MCP server %q yet; re-run shortly", mcpserverName)
	}
	endpoint := info.EndpointURL
	if endpoint == "" {
		if endpoint, err = mcpAggregateURLFromServerURL(serverURL, mcpserverName); err != nil {
			return nil, err
		}
	}
	return &mcpEndpoint{URL: endpoint, Token: info.Token}, nil
}

// currentServerURL is the server of the kubeconfig's current context.
func currentServerURL() (string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	kctx, ok := raw.Contexts[raw.CurrentContext]
	if !ok {
		return "", fmt.Errorf("no current context in kubeconfig; run 'faros login'")
	}
	cluster, ok := raw.Clusters[kctx.Cluster]
	if !ok || cluster.Server == "" {
		return "", fmt.Errorf("cluster %q of the current context has no server URL", kctx.Cluster)
	}
	return cluster.Server, nil
}

// tlsTrust says what a client needs to accept the endpoint's certificate.
type tlsTrust int

const (
	// trustSystem: the system roots already verify it.
	trustSystem tlsTrust = iota
	// trustCA: verifies once the --ca-file CA is added.
	trustCA
	// trustNone: nothing we have verifies it.
	trustNone
)

// probeEndpointTLS checks the endpoint's certificate against the system roots
// and then, if given, the system roots plus caFile. A CA file that still does
// not verify the certificate is an error: it is the wrong CA.
func probeEndpointTLS(endpoint, caFile string) (tlsTrust, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return trustNone, err
	}
	if u.Scheme != "https" {
		return trustSystem, nil // nothing to verify
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	system, err := x509.SystemCertPool()
	if err != nil {
		system = x509.NewCertPool()
	}
	return probeTLS(addr, u.Hostname(), system, caFile)
}

func probeTLS(addr, serverName string, system *x509.CertPool, caFile string) (tlsTrust, error) {
	dial := func(roots *x509.CertPool) error {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr,
			&tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		return conn.Close()
	}
	err := dial(system)
	if err == nil {
		return trustSystem, nil
	}
	var unknownAuthority x509.UnknownAuthorityError
	var certInvalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	isCertErr := errors.As(err, &unknownAuthority) || errors.As(err, &certInvalid) || errors.As(err, &hostname) ||
		strings.Contains(err.Error(), "certificate")
	if !isCertErr {
		return trustNone, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	if caFile == "" {
		return trustNone, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return trustNone, fmt.Errorf("reading --ca-file: %w", err)
	}
	roots := system.Clone()
	if !roots.AppendCertsFromPEM(pem) {
		return trustNone, fmt.Errorf("--ca-file %s holds no PEM certificate", caFile)
	}
	if err := dial(roots); err != nil {
		return trustNone, fmt.Errorf("--ca-file %s does not verify %s: %w", caFile, serverName, err)
	}
	return trustCA, nil
}

// claudeMCPArgs are the claude CLI invocations that (re)register the server.
func claudeMCPArgs(name, scope string, ep *mcpEndpoint) (remove, add []string) {
	remove = []string{"mcp", "remove", name, "--scope", scope}
	add = []string{"mcp", "add", "--transport", "http", "--scope", scope, name, ep.URL,
		"--header", "Authorization: Bearer " + ep.Token}
	return remove, add
}

// codexMCPArgs are the codex CLI invocations that (re)register the server.
func codexMCPArgs(name string, ep *mcpEndpoint) (remove, add []string) {
	remove = []string{"mcp", "remove", name}
	add = []string{"mcp", "add", name, "--url", ep.URL, "--bearer-token-env-var", codexTokenEnvVar}
	return remove, add
}

func runMCPClaude(out io.Writer, o *mcpClientOptions) error {
	switch o.scope {
	case "user", "local", "project":
	default:
		return fmt.Errorf("--scope must be user, local or project, got %q", o.scope)
	}
	ep, err := resolveMCPEndpoint(o.mcpserverName)
	if err != nil {
		return err
	}
	trust, err := probeEndpointTLS(ep.URL, o.caFile)
	if err != nil {
		return err
	}
	name := o.serverName()
	remove, add := claudeMCPArgs(name, o.scope, ep)
	ran, err := runClientCLI(out, "claude", remove, add, o.dryRun)
	if err != nil {
		return err
	}
	if ran {
		p(out, "Added MCP server %q (%s) to Claude Code, %s scope.\n", name, ep.URL, o.scope)
	}

	p(out, "\nStart Claude Code:\n")
	switch trust {
	case trustSystem:
		p(out, "  claude\n")
	case trustCA:
		ca := absPath(o.caFile)
		p(out, "  NODE_EXTRA_CA_CERTS=%s claude\n", shellSingleQuote(ca))
		p(out, "\nThe hub's certificate is signed by %s, which Claude Code trusts only when told to.\n", o.caFile)
		p(out, "To make that permanent, add it to the \"env\" block of ~/.claude/settings.json:\n")
		p(out, "  \"env\": { \"NODE_EXTRA_CA_CERTS\": %q }\n", ca)
	case trustNone:
		p(out, "  NODE_TLS_REJECT_UNAUTHORIZED=0 claude\n")
		p(out, "\nThe hub's certificate is not publicly trusted, so this turns TLS certificate\n")
		p(out, "verification off for everything that Claude Code session connects to, the\n")
		p(out, "Anthropic API included. Prefer trusting the hub's CA instead:\n")
		p(out, "  faros mcp claude --ca-file <hub CA>   (faros dev init writes <cluster>-ca.crt)\n")
	}
	p(out, "\nThen check the connection with: claude mcp list\n")
	return notInstalledErr("claude", ran, o.dryRun)
}

func runMCPCodex(out io.Writer, o *mcpClientOptions) error {
	ep, err := resolveMCPEndpoint(o.mcpserverName)
	if err != nil {
		return err
	}
	trust, err := probeEndpointTLS(ep.URL, o.caFile)
	if err != nil {
		return err
	}
	name := o.serverName()
	remove, add := codexMCPArgs(name, ep)
	ran, err := runClientCLI(out, "codex", remove, add, o.dryRun)
	if err != nil {
		return err
	}
	if ran {
		p(out, "Added MCP server %q (%s) to Codex.\n", name, ep.URL)
	}

	p(out, "\nStart Codex (it reads the MCP token from %s):\n", codexTokenEnvVar)
	p(out, "  export %s=%s\n", codexTokenEnvVar, shellSingleQuote(ep.Token))
	switch trust {
	case trustSystem:
		p(out, "  codex\n")
	case trustCA:
		bundle, err := writeCABundle(ep.URL, o.caFile)
		if err != nil {
			return err
		}
		p(out, "  CODEX_CA_CERTIFICATE=%s codex\n", shellSingleQuote(bundle))
		p(out, "\nThat bundle holds your system roots plus %s, so Codex keeps\n", o.caFile)
		p(out, "trusting its own API while accepting the hub. Needs Codex 0.129.0 or later.\n")
	case trustNone:
		p(out, "  codex\n")
		p(out, "\nWarning: the hub's certificate is not publicly trusted and Codex cannot skip\n")
		p(out, "certificate verification, so this server will fail to connect. Re-run with the\n")
		p(out, "CA that signs it:\n")
		p(out, "  faros mcp codex --ca-file <hub CA>   (faros dev init writes <cluster>-ca.crt)\n")
	}
	p(out, "\nThen check the connection with: codex mcp list\n")
	return notInstalledErr("codex", ran, o.dryRun)
}

// runClientCLI runs remove (ignoring failure: the entry may not exist) and
// then add with the client's CLI, reporting whether it ran them. For
// --dry-run, or when the client is not installed, it prints them instead.
func runClientCLI(out io.Writer, bin string, remove, add []string, dryRun bool) (bool, error) {
	path, lookErr := exec.LookPath(bin)
	if dryRun || lookErr != nil {
		if !dryRun {
			p(out, "%s is not on your PATH. Run these once it is:\n", bin)
		}
		p(out, "  %s %s\n", bin, shellJoin(remove))
		p(out, "  %s %s\n", bin, shellJoin(add))
		return false, nil
	}
	_ = exec.Command(path, remove...).Run() //nolint:gosec // our own argument list
	cmd := exec.Command(path, add...)       //nolint:gosec // our own argument list
	if msg, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("%s %s: %w\n%s", bin, strings.Join(add[:2], " "), err, strings.TrimSpace(string(msg)))
	}
	return true, nil
}

// notInstalledErr fails the command, after the instructions are printed, when
// the client was not there to configure.
func notInstalledErr(bin string, ran, dryRun bool) error {
	if ran || dryRun {
		return nil
	}
	return fmt.Errorf("%s not found on PATH; nothing was configured", bin)
}

// systemCABundleFiles are where distributions keep their PEM root bundle.
var systemCABundleFiles = []string{
	"/etc/ssl/cert.pem",                  // macOS, Alpine, some BSDs
	"/etc/ssl/certs/ca-certificates.crt", // Debian, Ubuntu
	"/etc/pki/tls/certs/ca-bundle.crt",   // Fedora, RHEL
	"/etc/ssl/ca-bundle.pem",             // openSUSE
}

// writeCABundle writes the system root bundle plus caFile to
// ~/.faros/ca/<endpoint host>.pem and returns the path. Codex uses a custom CA
// file in place of its default roots, so the bundle has to carry both.
func writeCABundle(endpoint, caFile string) (string, error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return "", fmt.Errorf("reading --ca-file: %w", err)
	}
	var system []byte
	candidates := systemCABundleFiles
	if env := os.Getenv("SSL_CERT_FILE"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	for _, f := range candidates {
		if b, err := os.ReadFile(f); err == nil && len(b) > 0 { //nolint:gosec // well-known system paths
			system = b
			break
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".faros", "ca")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	bundle := make([]byte, 0, len(system)+len(ca)+1)
	bundle = append(bundle, system...)
	if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, ca...)
	path := filepath.Join(dir, u.Hostname()+".pem")
	if err := os.WriteFile(path, bundle, 0o644); err != nil { //nolint:gosec // public certificates
		return "", err
	}
	return path, nil
}

func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// shellJoin quotes each argument that needs it, for copy-pasteable output.
func shellJoin(args []string) string {
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@", r)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		if a != "" && strings.IndexFunc(a, func(r rune) bool { return !safe(r) }) < 0 {
			quoted[i] = a
			continue
		}
		quoted[i] = shellSingleQuote(a)
	}
	return strings.Join(quoted, " ")
}

func p(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
