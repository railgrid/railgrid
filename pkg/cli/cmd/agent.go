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
	"syscall"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/faroshq/faros-kedge/pkg/agent"
)

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent management commands",
	}

	cmd.AddCommand(
		newAgentJoinCommand(),
		newAgentTokenCommand(),
		newAgentInstallCommand(),
		newAgentUninstallCommand(),
	)

	return cmd
}

func newAgentJoinCommand() *cobra.Command {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join an edge to the hub",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			a, err := agent.New(opts)
			if err != nil {
				return fmt.Errorf("failed to create agent: %w", err)
			}

			return a.Run(ctx)
		},
	}

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

	return cmd
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

const systemdUnitTemplate = `[Unit]
Description=Kedge Agent - {{.EdgeName}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} agent join \
  --hub-kubeconfig {{.HubKubeconfig}} \
  --edge-name {{.EdgeName}} \
  --type {{.Type}}{{if .SSHProxyPort}} \
  --ssh-proxy-port {{.SSHProxyPort}}{{end}}{{if .SSHUser}} \
  --ssh-user {{.SSHUser}}{{end}}{{if .SSHPrivateKey}} \
  --ssh-private-key {{.SSHPrivateKey}}{{end}}{{if .Cluster}} \
  --cluster {{.Cluster}}{{end}}{{if .InsecureSkipTLS}} \
  --hub-insecure-skip-tls-verify{{end}}
Restart=always
RestartSec=10
Environment=HOME=/root

[Install]
WantedBy=multi-user.target
`

type systemdUnitData struct {
	BinaryPath      string
	HubKubeconfig   string
	EdgeName        string
	Type            string
	SSHProxyPort    int
	SSHUser         string
	SSHPrivateKey   string
	Cluster         string
	InsecureSkipTLS bool
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
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install kedge agent as a systemd service",
		Long: `Install the kedge agent as a systemd service on the current host.

This creates a systemd unit file, reloads the daemon, enables and starts the
service. The systemd unit runs "kedge agent join" so you get both the agent
and the full kedge CLI on the server.

Requires root privileges.

Example:
  sudo kedge agent install \
    --hub-kubeconfig /etc/kedge/hub.kubeconfig \
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
				unitName = "kedge-agent-" + edgeName
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
	cmd.Flags().StringVar(&unitName, "unit-name", "", "Systemd unit name (default: kedge-agent-<edge-name>)")

	return cmd
}

func newAgentUninstallCommand() *cobra.Command {
	var unitName string
	var edgeName string

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall kedge agent systemd service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if unitName == "" {
				if edgeName == "" {
					return fmt.Errorf("--edge-name or --unit-name is required")
				}
				unitName = "kedge-agent-" + edgeName
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
	cmd.Flags().StringVar(&unitName, "unit-name", "", "Systemd unit name (default: kedge-agent-<edge-name>)")

	return cmd
}
