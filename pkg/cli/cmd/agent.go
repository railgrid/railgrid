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
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/agent"
	"github.com/faroshq/faros/pkg/agent/tunnel"
	pkgversion "github.com/faroshq/faros/pkg/version"
)

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent management commands",
	}

	cmd.AddCommand(
		newAgentJoinCommand(),
		newAgentRunCommand(),
		newAgentTokenCommand(),
		newAgentInstallCommand(),
		newAgentUninstallCommand(),
		newAgentUpgradeCommand(),
	)

	return cmd
}

// agentRunFlags attaches all agent runtime flags to cmd, populating opts.
// Shared between newAgentJoinCommand (install path) and newAgentRunCommand
// (foreground path).
func agentRunFlags(cmd *cobra.Command, opts *agent.Options) {
	cmd.Flags().StringVar(&opts.HubURL, "hub-url", "", "Hub server URL")
	cmd.Flags().StringVar(&opts.HubKubeconfig, "hub-kubeconfig", "", "Kubeconfig for hub cluster")
	cmd.Flags().StringVar(&opts.HubContext, "hub-context", "", "Kubeconfig context for hub cluster")
	cmd.Flags().StringVar(&opts.TunnelURL, "tunnel-url", "", "Hub tunnel URL (defaults to hub URL)")
	cmd.Flags().StringVar(&opts.Token, "token", "", "Bootstrap token")
	cmd.Flags().StringVar(&opts.EdgeName, "edge-name", "", "Name of this edge")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to target cluster kubeconfig")
	cmd.Flags().StringVar(&opts.Context, "context", "", "Kubeconfig context to use")
	cmd.Flags().StringToStringVar(&opts.Labels, "labels", nil, "Labels for this edge")
	cmd.Flags().BoolVar(&opts.InsecureSkipTLSVerify, "hub-insecure-skip-tls-verify", false, "Skip TLS certificate verification for the hub connection (insecure, for development only)")
	cmd.Flags().IntVar(&opts.SSHProxyPort, "ssh-proxy-port", 22, "Local port of the SSH daemon to proxy connections to (default 22; set to a different port in test environments)")
	cmd.Flags().StringVar((*string)(&opts.Type), "type", string(agent.AgentTypeKubernetes),
		`Edge type: "kubernetes" (Kubernetes cluster) or "server" (bare-metal/systemd host with SSH access)`)
	cmd.Flags().StringVar(&opts.Cluster, "cluster", "",
		"kcp logical cluster name (e.g. '1tww43gelbj45g0k'); required when using static token auth without a cluster-scoped hub kubeconfig")
	cmd.Flags().StringVar(&opts.SSHUser, "ssh-user", "", "SSH username for server-type edges (default: current user)")
	cmd.Flags().StringVar(&opts.SSHPassword, "ssh-password", "", "SSH password for password-based authentication (prefer --ssh-private-key for security)")
	cmd.Flags().StringVar(&opts.SSHPrivateKeyPath, "ssh-private-key", "", "Path to SSH private key file for key-based authentication")
	cmd.Flags().StringVar(&opts.DebugAddr, "debug-addr", "", "Bind address for the debug HTTP server exposing /healthz and /debug/pprof/* (e.g. \"127.0.0.1:6060\"). Empty disables the server.")
	cmd.Flags().StringSliceVar(&opts.SvcAllowedCIDRs, "svc-allow-cidr", svcAllowCIDRDefault(),
		"CIDR the Service proxy may dial besides loopback (and cluster DNS in kubernetes mode), e.g. 192.168.1.0/24. Repeatable. Link-local, unspecified and multicast addresses are never allowed. Env: "+svcAllowCIDREnv)
	cmd.Flags().StringVar(&opts.SvcPolicy, "svc-policy", svcPolicyDefault(),
		"What the Service proxy does with a target outside loopback/--svc-allow-cidr: enforce (403, never dialed), warn (dialed but logged; response carries X-Faros-Svc-Policy: warn) or allow-any (allow list disabled; logged at startup). The default flips to enforce in the next release. Env: "+svcPolicyEnv)
}

// Environment fallbacks for the Service proxy policy flags, so systemd units
// and in-cluster Deployments can set the policy without editing args.
const (
	svcAllowCIDREnv = "FAROS_AGENT_SVC_ALLOW_CIDR"
	svcPolicyEnv    = "FAROS_AGENT_SVC_POLICY"
)

// svcAllowCIDRDefault reads FAROS_AGENT_SVC_ALLOW_CIDR (comma-separated).
func svcAllowCIDRDefault() []string {
	var out []string
	for _, s := range strings.Split(os.Getenv(svcAllowCIDREnv), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// svcPolicyDefault reads FAROS_AGENT_SVC_POLICY, else tunnel.DefaultSvcPolicy.
func svcPolicyDefault() string {
	if v := strings.TrimSpace(os.Getenv(svcPolicyEnv)); v != "" {
		return v
	}
	return string(tunnel.DefaultSvcPolicy)
}

// svcPolicyArgs renders the Service proxy flags for an installed unit or
// Deployment. The policy is only rendered when it differs from the built-in
// default, so installs pick up the next release's default flip without a
// reinstall; allow CIDRs are always rendered.
func svcPolicyArgs(cidrs []string, policy string) []string {
	var args []string
	for _, c := range cidrs {
		args = append(args, "--svc-allow-cidr="+c)
	}
	if policy != "" && policy != string(tunnel.DefaultSvcPolicy) {
		args = append(args, "--svc-policy="+policy)
	}
	return args
}

// runAgentForeground contains the shared foreground-process logic used by both
// newAgentRunCommand and (transitionally) other paths that need a blocking agent.
func runAgentForeground(ctx context.Context, opts *agent.Options) error {
	logger := klog.FromContext(ctx)

	// Normalize hub URL: add https:// if no scheme provided.
	opts.HubURL = normalizeHubURL(opts.HubURL)

	// Token-exchange: if no bootstrap token was provided on the command line,
	// try to load a previously saved kubeconfig or durable token from disk.
	// This allows the agent to reconnect after the first successful join
	// without requiring the operator to re-supply the bootstrap join token.
	if opts.HubKubeconfig == "" && opts.EdgeName != "" {
		// In-cluster mode: check the kubeconfig Secret FIRST.  On the initial
		// run the Secret does not exist yet so we fall through to token-based
		// bootstrap.  After the first successful join the hub kubeconfig is
		// persisted in the Secret (see SaveKubeconfigToSecret / os.Exit(1)
		// restart dance).  On subsequent restarts we load the Secret here so
		// the agent never tries to re-use the already-cleared bootstrap token.
		if agent.IsInCluster() {
			kubeconfigData, err := agent.LoadKubeconfigFromSecret(opts.EdgeName)
			if err != nil {
				logger.Info("Could not load in-cluster kubeconfig Secret (will try other sources)", "err", err)
			} else if kubeconfigData != "" {
				// Write to a temp file so the rest of the startup path can use it.
				tmpFile, err := os.CreateTemp("", "faros-agent-kubeconfig-*")
				if err != nil {
					return fmt.Errorf("creating temp kubeconfig file: %w", err)
				}
				if _, err := tmpFile.WriteString(kubeconfigData); err != nil {
					return fmt.Errorf("writing temp kubeconfig: %w", err)
				}
				if closeErr := tmpFile.Close(); closeErr != nil {
					return fmt.Errorf("closing temp kubeconfig file: %w", closeErr)
				}
				logger.Info("Using hub kubeconfig from in-cluster Secret", "edgeName", opts.EdgeName, "secret", agent.AgentKubeconfigSecretName(opts.EdgeName))
				opts.HubKubeconfig = tmpFile.Name()
				opts.Token = "" // Secret kubeconfig takes precedence over bootstrap token.
				opts.UsingSavedKubeconfig = true
			}
		}
	}

	if opts.HubKubeconfig == "" && opts.EdgeName != "" {
		// Prefer a saved kubeconfig (from a previous join-token exchange) over
		// the bootstrap token. This allows the agent to reconnect after restart
		// even when --token is still present in the systemd unit.
		kubeconfigPath, err := agent.LoadAgentKubeconfig(opts.EdgeName)
		if err != nil {
			logger.Info("Could not check for saved agent kubeconfig", "err", err)
		} else if kubeconfigPath != "" {
			if err := agent.ValidateAgentKubeconfig(kubeconfigPath, opts.InsecureSkipTLSVerify); err != nil {
				logger.Info("Saved agent kubeconfig is invalid, deleting stale file",
					"edgeName", opts.EdgeName, "path", kubeconfigPath, "err", err)
				if delErr := agent.DeleteAgentKubeconfig(opts.EdgeName); delErr != nil {
					logger.Error(delErr, "Failed to delete stale agent kubeconfig")
				}
			} else {
				logger.Info("Using saved agent kubeconfig from previous registration", "edgeName", opts.EdgeName, "path", kubeconfigPath)
				opts.HubKubeconfig = kubeconfigPath
				opts.Token = "" // Clear join token; SA kubeconfig takes precedence.
				opts.UsingSavedKubeconfig = true
			}
		} else {
			// Fallback: legacy token config.
			saved, err := agent.LoadAgentConfig(opts.EdgeName)
			if err != nil {
				logger.Info("Could not load saved agent config (will require --token)", "err", err)
			} else if saved != nil && saved.Token != "" {
				logger.Info("Loaded durable agent token from saved config; no --token needed", "edgeName", opts.EdgeName)
				opts.Token = saved.Token
				if opts.HubURL == "" && saved.HubURL != "" {
					opts.HubURL = saved.HubURL
				}
			}
		}
	}

	a, err := agent.New(opts)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}
	return a.Run(ctx)
}

// newAgentRunCommand returns the "faros agent run" command — a foreground
// process that connects this edge to the hub and blocks until interrupted.
// This is the command used by containers, e2e tests, and dev workflows.
// For persistent installation (systemd service), use "faros agent join".
func newAgentRunCommand() *cobra.Command {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)",
		Long: `Run the faros agent as a blocking foreground process.

The agent connects to the hub, registers this edge, and maintains the reverse
tunnel until interrupted (SIGINT/SIGTERM). Suitable for containers, e2e tests,
and interactive development.

For production use on bare-metal or VM hosts, use "faros agent join" instead,
which installs the agent as a persistent systemd service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runAgentForeground(ctx, opts)
		},
	}

	agentRunFlags(cmd, opts)
	return cmd
}

// newAgentJoinCommand returns the "faros agent join" command — a persistent
// install that registers this edge with the hub and ensures the agent keeps
// running across reboots.
//
//   - server type:     installs a systemd service (requires root)
//   - kubernetes type: applies a Deployment + RBAC into the target cluster
func newAgentJoinCommand() *cobra.Command {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Persistently join an edge to the hub (installs systemd service or Kubernetes Deployment)",
		Long: `Join this edge to the hub as a persistent installation.

For server-type edges (bare-metal / VM):
  Installs a systemd service that runs "faros agent run" and survives reboots.
  Requires root. The service is named faros-agent-<edge-name>.service.

For kubernetes-type edges:
  Applies a Deployment and RBAC into the faros-agent namespace of the target
  cluster so the agent runs as an in-cluster workload.

To run the agent as a foreground process (containers / dev / e2e) use:
  faros agent run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.EdgeName == "" {
				return fmt.Errorf("--edge-name is required")
			}
			if opts.HubKubeconfig == "" && opts.Token == "" {
				return fmt.Errorf("--hub-kubeconfig or --token is required")
			}

			// Normalize hub URL: add https:// if no scheme provided.
			opts.HubURL = normalizeHubURL(opts.HubURL)

			switch opts.Type {
			case agent.AgentTypeServer, "":
				return agentJoinServer(opts)
			case agent.AgentTypeKubernetes:
				return agentJoinKubernetes(opts)
			default:
				return fmt.Errorf("unknown agent type %q; must be 'server' or 'kubernetes'", opts.Type)
			}
		},
	}

	agentRunFlags(cmd, opts)
	return cmd
}

// agentJoinServer installs the agent as a systemd service on the current host.
func agentJoinServer(opts *agent.Options) error {
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	absKubeconfig := opts.HubKubeconfig
	if absKubeconfig != "" {
		absKubeconfig, err = filepath.Abs(absKubeconfig)
		if err != nil {
			return fmt.Errorf("resolving kubeconfig path: %w", err)
		}
	}

	unitName := "faros-agent-" + opts.EdgeName
	data, err := joinServerUnitData(opts, binaryPath, absKubeconfig)
	if err != nil {
		return err
	}

	tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
	if err != nil {
		return fmt.Errorf("parsing unit template: %w", err)
	}

	unitPath := "/etc/systemd/system/" + unitName + ".service"
	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("creating unit file %s: %w (are you running as root?)", unitPath, err)
	}
	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing unit file: %w", err)
	}
	_ = f.Close()

	fmt.Printf("Systemd unit written to %s\n", unitPath)

	for _, c := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", unitName + ".service"},
		{"systemctl", "start", unitName + ".service"},
	} {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("running %v: %w\n%s", c, err, out)
		}
	}

	fmt.Printf("Agent installed and running as systemd service.\n")
	fmt.Printf("  Check status:  systemctl status %s\n", unitName)
	fmt.Printf("  View logs:     journalctl -u %s -f\n", unitName)
	fmt.Printf("  Uninstall:     faros agent uninstall --edge-name %s\n", opts.EdgeName)
	return nil
}

// joinServerUnitData builds the systemd unit data for `faros agent join
// --type server` from the run flags. The Service proxy policy flags are
// carried into the unit exactly as `agent install` does: the allow list
// always, the policy only when it differs from the built-in default. Leaving
// them out would silently run the installed agent at the default policy with
// no allow list while the operator believes they configured enforce.
func joinServerUnitData(opts *agent.Options, binaryPath, absKubeconfig string) (systemdUnitData, error) {
	data := systemdUnitData{
		BinaryPath:      binaryPath,
		HubKubeconfig:   absKubeconfig,
		HubURL:          opts.HubURL,
		Token:           opts.Token,
		EdgeName:        opts.EdgeName,
		Type:            string(opts.Type),
		SSHProxyPort:    opts.SSHProxyPort,
		SSHUser:         opts.SSHUser,
		SSHPrivateKey:   opts.SSHPrivateKeyPath,
		Cluster:         opts.Cluster,
		InsecureSkipTLS: opts.InsecureSkipTLSVerify,
		SvcAllowCIDRs:   opts.SvcAllowedCIDRs,
	}
	if opts.SvcPolicy != "" && opts.SvcPolicy != string(tunnel.DefaultSvcPolicy) {
		if _, err := tunnel.ParseSvcPolicy(opts.SvcPolicy); err != nil {
			return systemdUnitData{}, err
		}
		data.SvcPolicy = opts.SvcPolicy
	}
	if _, err := tunnel.ParseSvcAllowedCIDRs(opts.SvcAllowedCIDRs); err != nil {
		return systemdUnitData{}, err
	}
	return data, nil
}

// agentJoinKubernetes applies a Deployment + RBAC to the target cluster so the
// agent runs as a persistent in-cluster workload.
//
// Two auth modes are supported:
//   - Join-token mode (--token):     Deployment runs with --token flag; no kubeconfig Secret needed.
//   - Kubeconfig mode (--hub-kubeconfig): Deployment mounts a Secret containing the hub kubeconfig.
func agentJoinKubernetes(opts *agent.Options) error {
	if opts.HubKubeconfig == "" && opts.Token == "" {
		return fmt.Errorf("--hub-kubeconfig or --token is required for kubernetes-type join")
	}

	usingToken := opts.Token != ""

	// Determine the target cluster kubeconfig for kubectl.
	kubectlArgs := []string{}
	if opts.Kubeconfig != "" {
		kubectlArgs = append(kubectlArgs, "--kubeconfig", opts.Kubeconfig)
	}
	if opts.Context != "" {
		kubectlArgs = append(kubectlArgs, "--context", opts.Context)
	}

	// Ensure faros-agent namespace exists.
	nsManifest := `apiVersion: v1
kind: Namespace
metadata:
  name: faros-agent
`
	if err := kubectlApplyManifest(kubectlArgs, nsManifest); err != nil {
		return fmt.Errorf("creating faros-agent namespace: %w", err)
	}

	// ServiceAccount.
	saManifest := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: faros-agent-%s
  namespace: faros-agent
`, opts.EdgeName)
	if err := kubectlApplyManifest(kubectlArgs, saManifest); err != nil {
		return fmt.Errorf("creating ServiceAccount: %w", err)
	}

	// ClusterRole — shared across all edges; grants the agent cluster-wide workload permissions.
	clusterRoleManifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: faros-edge-agent
rules:
- apiGroups: [""]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["apps"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["batch"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["networking.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["storage.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["apiextensions.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
- apiGroups: ["coordination.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
`
	if err := kubectlApplyManifest(kubectlArgs, clusterRoleManifest); err != nil {
		return fmt.Errorf("creating ClusterRole: %w", err)
	}

	// ClusterRoleBinding — binds the edge-specific SA to the shared ClusterRole.
	crbManifest := fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: faros-edge-agent-%s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: faros-edge-agent
subjects:
- kind: ServiceAccount
  name: faros-agent-%s
  namespace: faros-agent
`, opts.EdgeName, opts.EdgeName)
	if err := kubectlApplyManifest(kubectlArgs, crbManifest); err != nil {
		return fmt.Errorf("creating ClusterRoleBinding: %w", err)
	}

	// Role — allows the agent to manage its own kubeconfig Secret.
	roleManifest := fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: faros-agent-%s
  namespace: faros-agent
rules:
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: ["faros-agent-%s-kubeconfig"]
  verbs: ["get", "create", "update", "patch"]
`, opts.EdgeName, opts.EdgeName)
	if err := kubectlApplyManifest(kubectlArgs, roleManifest); err != nil {
		return fmt.Errorf("creating Role: %w", err)
	}

	// RoleBinding.
	rbManifest := fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: faros-agent-%s
  namespace: faros-agent
subjects:
- kind: ServiceAccount
  name: faros-agent-%s
  namespace: faros-agent
roleRef:
  kind: Role
  name: faros-agent-%s
  apiGroup: rbac.authorization.k8s.io
`, opts.EdgeName, opts.EdgeName, opts.EdgeName)
	if err := kubectlApplyManifest(kubectlArgs, rbManifest); err != nil {
		return fmt.Errorf("creating RoleBinding: %w", err)
	}

	agentImage := os.Getenv("FAROS_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "ghcr.io/faroshq/faros-agent"
	}
	agentImageTag := os.Getenv("FAROS_AGENT_IMAGE_TAG")
	if agentImageTag == "" {
		agentImageTag = pkgversion.Get()
	}
	agentImagePullPolicy := os.Getenv("FAROS_AGENT_IMAGE_PULL_POLICY")
	if agentImagePullPolicy == "" {
		agentImagePullPolicy = "IfNotPresent"
	}
	image := agentImage + ":" + agentImageTag

	var deployManifest string

	if usingToken {
		// Token-based bootstrap: pass --token directly in Deployment args.
		// The agent exchanges the join token for a kubeconfig on first connect;
		// no hub kubeconfig Secret is required.
		hubURL := opts.HubURL
		if hubURL == "" {
			return fmt.Errorf("--hub-url is required when using --token for kubernetes-type join")
		}
		// faros-agent is a standalone binary; flags are passed directly (no subcommands).
		deployArgs := fmt.Sprintf("--hub-url=%s --edge-name=%s --type=kubernetes --token=%s",
			hubURL, opts.EdgeName, opts.Token)
		if opts.InsecureSkipTLSVerify {
			deployArgs += " --hub-insecure-skip-tls-verify"
		}
		if opts.Cluster != "" {
			deployArgs += " --cluster=" + opts.Cluster
		}
		for _, a := range svcPolicyArgs(opts.SvcAllowedCIDRs, opts.SvcPolicy) {
			deployArgs += " " + a
		}
		deployManifest = fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: faros-agent-%s
  namespace: faros-agent
  labels:
    app: faros-agent
    faros.sh/edge-name: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: faros-agent
      faros.sh/edge-name: %s
  template:
    metadata:
      labels:
        app: faros-agent
        faros.sh/edge-name: %s
    spec:
      serviceAccountName: faros-agent-%s
      containers:
      - name: agent
        image: %s
        imagePullPolicy: %s
        env:
        - name: HOME
          value: /tmp
        args: [%s]
`,
			opts.EdgeName, opts.EdgeName, opts.EdgeName, opts.EdgeName,
			opts.EdgeName, image, agentImagePullPolicy,
			formatDeployArgs(deployArgs))
	} else {
		// Kubeconfig-based: mount a Secret containing the hub kubeconfig.
		absKubeconfig, err := filepath.Abs(opts.HubKubeconfig)
		if err != nil {
			return fmt.Errorf("resolving hub kubeconfig path: %w", err)
		}
		kubeconfigData, err := os.ReadFile(absKubeconfig)
		if err != nil {
			return fmt.Errorf("reading hub kubeconfig: %w", err)
		}
		secretName := "faros-agent-" + opts.EdgeName + "-hub-kubeconfig"
		secretManifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: faros-agent
type: Opaque
stringData:
  hub.kubeconfig: |
%s`, secretName, indentLines(string(kubeconfigData), "    "))
		if err := kubectlApplyManifest(kubectlArgs, secretManifest); err != nil {
			return fmt.Errorf("creating hub kubeconfig secret: %w", err)
		}

		// faros-agent is a standalone binary; flags are passed directly (no subcommands).
		deployArgs := fmt.Sprintf("--hub-kubeconfig=/etc/faros/hub.kubeconfig --edge-name=%s --type=kubernetes", opts.EdgeName)
		if opts.InsecureSkipTLSVerify {
			deployArgs += " --hub-insecure-skip-tls-verify"
		}
		if opts.Cluster != "" {
			deployArgs += " --cluster=" + opts.Cluster
		}
		for _, a := range svcPolicyArgs(opts.SvcAllowedCIDRs, opts.SvcPolicy) {
			deployArgs += " " + a
		}
		deployManifest = fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: faros-agent-%s
  namespace: faros-agent
  labels:
    app: faros-agent
    faros.sh/edge-name: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: faros-agent
      faros.sh/edge-name: %s
  template:
    metadata:
      labels:
        app: faros-agent
        faros.sh/edge-name: %s
    spec:
      serviceAccountName: faros-agent-%s
      containers:
      - name: agent
        image: %s
        imagePullPolicy: %s
        env:
        - name: HOME
          value: /tmp
        args: [%s]
        volumeMounts:
        - name: hub-kubeconfig
          mountPath: /etc/faros
          readOnly: true
      volumes:
      - name: hub-kubeconfig
        secret:
          secretName: %s
`,
			opts.EdgeName, opts.EdgeName, opts.EdgeName, opts.EdgeName,
			opts.EdgeName, image, agentImagePullPolicy,
			formatDeployArgs(deployArgs),
			secretName)
	}

	if err := kubectlApplyManifest(kubectlArgs, deployManifest); err != nil {
		return fmt.Errorf("creating Deployment: %w", err)
	}

	fmt.Printf("✓ faros-agent deployed to Kubernetes\n")
	fmt.Printf("  Namespace: faros-agent\n")
	fmt.Printf("  Check status: kubectl get pods -n faros-agent\n")
	fmt.Printf("  Logs:         kubectl logs -n faros-agent deploy/faros-agent-%s -f\n", opts.EdgeName)
	return nil
}

// kubectlApplyManifest writes manifest to a temp file and runs kubectl apply.
func kubectlApplyManifest(extraArgs []string, manifest string) error {
	f, err := os.CreateTemp("", "faros-join-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	if _, err := f.WriteString(manifest); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()

	args := append(extraArgs, "apply", "-f", f.Name())
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %w\n%s", err, out)
	}
	return nil
}

// indentLines prepends prefix to every line in s.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// formatDeployArgs converts a flat flag string into quoted kubectl args list.
func formatDeployArgs(s string) string {
	parts := strings.Fields(s)
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return strings.Join(quoted, ", ")
}

func newAgentTokenCommand() *cobra.Command {
	var edgeName string

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage agent tokens",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bootstrap token for an edge",
		RunE: func(cmd *cobra.Command, args []string) error {
			if edgeName == "" {
				return fmt.Errorf("--edge-name is required")
			}

			// TODO: Generate bootstrap token
			fmt.Printf("Bootstrap token for edge %s (not yet implemented)\n", edgeName)
			return nil
		},
	}
	createCmd.Flags().StringVar(&edgeName, "edge-name", "", "Edge name")

	cmd.AddCommand(createCmd)
	return cmd
}

// systemdUnitTemplate renders the systemd service unit for the faros agent.
//
// Token lifecycle note: when a join token is embedded (--token / --hub-url), the
// agent will exchange it for a hub kubeconfig on first connect and save it to
// $HOME/.faros/agent-<edge-name>.kubeconfig.  On every subsequent start the agent
// binary detects the saved kubeconfig (cmd/faros-agent/main.go checks
// LoadAgentKubeconfig before using the token) and clears --token automatically,
// so the unit file does not need to be rewritten after first registration.
const systemdUnitTemplate = `[Unit]
Description=Faros Agent - {{.EdgeName}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} agent run \
{{- if .Token}}
  --hub-url {{.HubURL}} \
  --token {{.Token}} \
{{- else}}
  --hub-kubeconfig {{.HubKubeconfig}} \
{{- end}}
  --edge-name {{.EdgeName}} \
  --type {{.Type}}{{if .SSHProxyPort}} \
  --ssh-proxy-port {{.SSHProxyPort}}{{end}}{{if .SSHUser}} \
  --ssh-user {{.SSHUser}}{{end}}{{if .SSHPrivateKey}} \
  --ssh-private-key {{.SSHPrivateKey}}{{end}}{{if .Cluster}} \
  --cluster {{.Cluster}}{{end}}{{if .InsecureSkipTLS}} \
  --hub-insecure-skip-tls-verify{{end}}{{range .SvcAllowCIDRs}} \
  --svc-allow-cidr {{.}}{{end}}{{if .SvcPolicy}} \
  --svc-policy {{.SvcPolicy}}{{end}}
Restart=always
RestartSec=10
Environment=HOME=/root

[Install]
WantedBy=multi-user.target
`

type systemdUnitData struct {
	BinaryPath      string
	HubKubeconfig   string
	HubURL          string
	Token           string
	EdgeName        string
	Type            string
	SSHProxyPort    int
	SSHUser         string
	SSHPrivateKey   string
	Cluster         string
	InsecureSkipTLS bool
	// SvcAllowCIDRs / SvcPolicy render --svc-allow-cidr / --svc-policy. SvcPolicy
	// is left empty when it equals the built-in default (see svcPolicyArgs).
	SvcAllowCIDRs []string
	SvcPolicy     string
}

func newAgentInstallCommand() *cobra.Command {
	var (
		hubKubeconfig   string
		edgeName        string
		edgeType        string
		sshProxyPort    int
		sshUser         string
		sshPrivateKey   string
		cluster         string
		insecureSkipTLS bool
		unitName        string
		svcAllowCIDRs   []string
		svcPolicy       string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install faros agent as a systemd service",
		Long: `Install the faros agent as a systemd service on the current host.

This creates a systemd unit file, reloads the daemon, enables and starts the
service. The systemd unit runs "faros agent run" so you get both the agent
and the full faros CLI on the server.

Requires root privileges.

Example:
  sudo faros agent install \
    --hub-kubeconfig /etc/faros/hub.kubeconfig \
    --edge-name my-server \
    --type server`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if edgeName == "" {
				return fmt.Errorf("--edge-name is required")
			}
			if hubKubeconfig == "" {
				return fmt.Errorf("--hub-kubeconfig is required")
			}

			// Resolve binary path.
			binaryPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolving binary path: %w", err)
			}
			binaryPath, err = filepath.EvalSymlinks(binaryPath)
			if err != nil {
				return fmt.Errorf("resolving symlinks: %w", err)
			}

			// Resolve kubeconfig to absolute path.
			absKubeconfig, err := filepath.Abs(hubKubeconfig)
			if err != nil {
				return fmt.Errorf("resolving kubeconfig path: %w", err)
			}

			if unitName == "" {
				unitName = "faros-agent-" + edgeName
			}

			data := systemdUnitData{
				BinaryPath:      binaryPath,
				HubKubeconfig:   absKubeconfig,
				EdgeName:        edgeName,
				Type:            edgeType,
				SSHProxyPort:    sshProxyPort,
				SSHUser:         sshUser,
				SSHPrivateKey:   sshPrivateKey,
				Cluster:         cluster,
				InsecureSkipTLS: insecureSkipTLS,
				SvcAllowCIDRs:   svcAllowCIDRs,
			}
			if svcPolicy != "" && svcPolicy != string(tunnel.DefaultSvcPolicy) {
				if _, err := tunnel.ParseSvcPolicy(svcPolicy); err != nil {
					return err
				}
				data.SvcPolicy = svcPolicy
			}
			if _, err := tunnel.ParseSvcAllowedCIDRs(svcAllowCIDRs); err != nil {
				return err
			}

			// Render systemd unit.
			tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
			if err != nil {
				return fmt.Errorf("parsing unit template: %w", err)
			}

			unitPath := "/etc/systemd/system/" + unitName + ".service"
			f, err := os.Create(unitPath)
			if err != nil {
				return fmt.Errorf("creating unit file %s: %w (are you running as root?)", unitPath, err)
			}
			if err := tmpl.Execute(f, data); err != nil {
				_ = f.Close()
				return fmt.Errorf("writing unit file: %w", err)
			}
			_ = f.Close()

			fmt.Printf("Systemd unit written to %s\n", unitPath)

			// Reload, enable, start.
			for _, c := range [][]string{
				{"systemctl", "daemon-reload"},
				{"systemctl", "enable", unitName + ".service"},
				{"systemctl", "start", unitName + ".service"},
			} {
				out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
				if err != nil {
					return fmt.Errorf("running %v: %w\n%s", c, err, out)
				}
			}

			fmt.Printf("Service %s installed, enabled, and started.\n", unitName)
			fmt.Printf("  Check status:  systemctl status %s\n", unitName)
			fmt.Printf("  View logs:     journalctl -u %s -f\n", unitName)
			return nil
		},
	}

	cmd.Flags().StringVar(&hubKubeconfig, "hub-kubeconfig", "", "Path to hub kubeconfig file (required)")
	cmd.Flags().StringVar(&edgeName, "edge-name", "", "Name of this edge (required)")
	cmd.Flags().StringVar(&edgeType, "type", "server", "Edge type: kubernetes or server")
	cmd.Flags().IntVar(&sshProxyPort, "ssh-proxy-port", 22, "Local SSH daemon port")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH username")
	cmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	cmd.Flags().StringVar(&cluster, "cluster", "", "kcp logical cluster path")
	cmd.Flags().BoolVar(&insecureSkipTLS, "hub-insecure-skip-tls-verify", false, "Skip TLS verification")
	cmd.Flags().StringVar(&unitName, "unit-name", "", "Systemd unit name (default: faros-agent-<edge-name>)")
	cmd.Flags().StringSliceVar(&svcAllowCIDRs, "svc-allow-cidr", svcAllowCIDRDefault(), "CIDR the Service proxy may dial besides loopback, e.g. 192.168.1.0/24 (repeatable; rendered into the unit)")
	cmd.Flags().StringVar(&svcPolicy, "svc-policy", svcPolicyDefault(), "Service proxy policy for targets outside the allowed set: enforce, warn or allow-any (rendered into the unit only when not the default)")

	return cmd
}

func newAgentUninstallCommand() *cobra.Command {
	var unitName string
	var edgeName string

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall faros agent systemd service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if unitName == "" {
				if edgeName == "" {
					return fmt.Errorf("--edge-name or --unit-name is required")
				}
				unitName = "faros-agent-" + edgeName
			}

			serviceName := unitName + ".service"

			// Stop and disable.
			for _, c := range [][]string{
				{"systemctl", "stop", serviceName},
				{"systemctl", "disable", serviceName},
			} {
				out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: %v: %s\n", c, out)
				}
			}

			// Remove unit file.
			unitPath := "/etc/systemd/system/" + serviceName
			if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s: %w", unitPath, err)
			}

			// Reload daemon.
			out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput()
			if err != nil {
				return fmt.Errorf("daemon-reload: %w\n%s", err, out)
			}

			fmt.Printf("Service %s uninstalled.\n", unitName)
			return nil
		},
	}

	cmd.Flags().StringVar(&edgeName, "edge-name", "", "Edge name (used to derive unit name)")
	cmd.Flags().StringVar(&unitName, "unit-name", "", "Systemd unit name (default: faros-agent-<edge-name>)")

	return cmd
}
