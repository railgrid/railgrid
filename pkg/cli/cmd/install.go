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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

type installOptions struct {
	installType string // "server" or "kubernetes"
	hubURL      string
	edgeName    string
	token       string
	kubeconfig  string // for --type=kubernetes: kubectl context kubeconfig
	dryRun      bool
}

func newInstallCommand() *cobra.Command {
	opts := &installOptions{}

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the kedge agent",
		Long: `Install the kedge agent on the current host (--type server) or on the
Kubernetes cluster addressed by the current kubeconfig context (--type kubernetes).

Examples:

  # Install as a systemd service on this server:
  kedge install --type server \
    --hub-url https://kedge.example.com \
    --edge-name my-edge \
    --token <join-token>

  # Generate and apply a Kubernetes Deployment for this cluster:
  kedge install --type kubernetes \
    --hub-url https://kedge.example.com \
    --edge-name my-edge \
    --token <join-token>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.token == "" {
				return fmt.Errorf("--token is required")
			}
			if opts.hubURL == "" {
				return fmt.Errorf("--hub-url is required")
			}
			if opts.edgeName == "" {
				return fmt.Errorf("--edge-name is required")
			}
			switch opts.installType {
			case "server":
				return installServer(opts)
			case "kubernetes":
				return installKubernetes(opts)
			default:
				return fmt.Errorf("unknown --type %q: must be 'server' or 'kubernetes'", opts.installType)
			}
		},
	}

	cmd.Flags().StringVar(&opts.installType, "type", "kubernetes", "Installation type: 'server' (systemd) or 'kubernetes' (kubectl apply)")
	cmd.Flags().StringVar(&opts.hubURL, "hub-url", "", "Hub server URL")
	cmd.Flags().StringVar(&opts.edgeName, "edge-name", "", "Name of this edge")
	cmd.Flags().StringVar(&opts.token, "token", "", "Bootstrap join token")
	cmd.Flags().StringVar(&opts.kubeconfig, "kubeconfig", "", "Path to kubeconfig for --type=kubernetes (default: $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print what would be done without applying it")

	return cmd
}

// installServer installs the kedge agent as a systemd service on the current host.
func installServer(opts *installOptions) error {
	if runtime.GOOS != "linux" {
		fmt.Printf("⚠ systemd installation is only supported on Linux.\n")
		fmt.Printf("On this platform (%s), run the agent directly:\n\n", runtime.GOOS)
		printAgentRunCmd(opts)
		return nil
	}

	configDir := "/etc/kedge"
	configPath := filepath.Join(configDir, "agent.yaml")
	servicePath := "/etc/systemd/system/kedge-agent.service"

	agentConfig := fmt.Sprintf("hub-url: %s\nedge-name: %s\ntoken: %s\n", opts.hubURL, opts.edgeName, opts.token)

	serviceUnit := fmt.Sprintf(`[Unit]
Description=Kedge Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/kedge agent join --config %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, configPath)

	if opts.dryRun {
		fmt.Printf("--- (dry-run) Would write %s ---\n%s\n", configPath, agentConfig)
		fmt.Printf("--- (dry-run) Would write %s ---\n%s\n", servicePath, serviceUnit)
		fmt.Println("--- (dry-run) Would run: systemctl daemon-reload && systemctl enable --now kedge-agent ---")
		return nil
	}

	// Write config
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", configDir, err)
	}
	//nolint:gosec // config file; 0640 keeps it group-readable
	if err := os.WriteFile(configPath, []byte(agentConfig), 0640); err != nil {
		return fmt.Errorf("writing agent config to %s: %w", configPath, err)
	}
	fmt.Printf("✓ Wrote agent config to %s\n", configPath)

	// Write service unit
	//nolint:gosec // service unit file; not sensitive
	if err := os.WriteFile(servicePath, []byte(serviceUnit), 0644); err != nil {
		return fmt.Errorf("writing systemd service to %s: %w (run as root?)", servicePath, err)
	}
	fmt.Printf("✓ Wrote systemd service to %s\n", servicePath)

	// Enable + start
	if isSystemdAvailable() {
		for _, args := range [][]string{
			{"daemon-reload"},
			{"enable", "--now", "kedge-agent"},
		} {
			//nolint:gosec // systemctl is a known system command
			if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, out)
			}
		}
		fmt.Println()
		fmt.Println("✓ kedge-agent installed and started")
		fmt.Println("  Status: systemctl status kedge-agent")
		fmt.Println("  Logs:   journalctl -u kedge-agent -f")
	} else {
		fmt.Println("\n⚠ systemd not detected. Start the agent manually:")
		printAgentRunCmd(opts)
	}
	return nil
}

// printAgentRunCmd prints the foreground agent run command.
func printAgentRunCmd(opts *installOptions) {
	fmt.Printf("  kedge agent run \\\n")
	fmt.Printf("    --hub-url %s \\\n", opts.hubURL)
	fmt.Printf("    --edge-name %s \\\n", opts.edgeName)
	fmt.Printf("    --type %s \\\n", opts.installType)
	fmt.Printf("    --token %s\n", opts.token)
}

// isSystemdAvailable returns true when systemd is running as PID 1.
func isSystemdAvailable() bool {
	link, err := os.Readlink("/proc/1/exe")
	if err != nil {
		return false
	}
	return strings.Contains(link, "systemd")
}

// agentManifestTemplate is a Go template for the Kubernetes agent manifest.
const agentManifestTemplate = `---
apiVersion: v1
kind: Namespace
metadata:
  name: kedge-agent
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
---
apiVersion: v1
kind: Secret
metadata:
  name: kedge-agent-{{ .EdgeName }}-kubeconfig
  namespace: kedge-agent
type: Opaque
data: {}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kedge-edge-agent
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
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kedge-edge-agent-{{ .EdgeName }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kedge-edge-agent
subjects:
- kind: ServiceAccount
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
rules:
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: ["kedge-agent-{{ .EdgeName }}-kubeconfig"]
  verbs: ["get", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
subjects:
- kind: ServiceAccount
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
roleRef:
  kind: Role
  name: kedge-agent-{{ .EdgeName }}
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kedge-agent-{{ .EdgeName }}
  namespace: kedge-agent
  labels:
    app: kedge-agent
    kedge.faros.sh/edge: {{ .EdgeName }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kedge-agent
      kedge.faros.sh/edge: {{ .EdgeName }}
  template:
    metadata:
      labels:
        app: kedge-agent
        kedge.faros.sh/edge: {{ .EdgeName }}
    spec:
      serviceAccountName: kedge-agent-{{ .EdgeName }}
      containers:
        - name: kedge-agent
          image: {{.Image}}:{{.ImageTag}}
          imagePullPolicy: {{.ImagePullPolicy}}
          env:
            - name: HOME
              value: /tmp
          args:
            - --hub-url={{ .HubURL }}
            - --edge-name={{ .EdgeName }}
            - --type=kubernetes
            - --token={{ .Token }}
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 256Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
`

// installKubernetes generates and (optionally) applies a Kubernetes Deployment
// for the kedge agent on the cluster targeted by the current kubeconfig context.
func installKubernetes(opts *installOptions) error {
	tmpl, err := template.New("agent-manifest").Parse(agentManifestTemplate)
	if err != nil {
		return fmt.Errorf("parsing manifest template: %w", err)
	}

	agentImage := os.Getenv("KEDGE_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "ghcr.io/faroshq/kedge-agent"
	}
	agentImageTag := os.Getenv("KEDGE_AGENT_IMAGE_TAG")
	if agentImageTag == "" {
		agentImageTag = "latest"
	}
	agentImagePullPolicy := os.Getenv("KEDGE_AGENT_IMAGE_PULL_POLICY")
	if agentImagePullPolicy == "" {
		agentImagePullPolicy = "IfNotPresent"
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, map[string]string{
		"HubURL":          opts.hubURL,
		"EdgeName":        opts.edgeName,
		"Token":           opts.token,
		"Image":           agentImage,
		"ImageTag":        agentImageTag,
		"ImagePullPolicy": agentImagePullPolicy,
	}); err != nil {
		return fmt.Errorf("rendering manifest: %w", err)
	}
	manifest := sb.String()

	if opts.dryRun {
		fmt.Println("--- (dry-run) Would apply the following manifest ---")
		fmt.Println(manifest)
		return nil
	}

	// Try to apply via kubectl if available.
	kubectlArgs := []string{"apply", "-f", "-"}
	if opts.kubeconfig != "" {
		kubectlArgs = append(kubectlArgs, "--kubeconfig", opts.kubeconfig)
	}

	//nolint:gosec // kubectl is a known command; args are validated above
	kubectlCmd := exec.Command("kubectl", kubectlArgs...)
	kubectlCmd.Stdin = strings.NewReader(manifest)
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr

	if err := kubectlCmd.Run(); err != nil {
		// kubectl not found or failed: print manifest so the user can apply manually.
		fmt.Println("Could not run kubectl. Apply the following manifest manually:")
		fmt.Println()
		fmt.Println(manifest)
		fmt.Println("Apply with: kubectl apply -f -")
		return nil
	}

	fmt.Println()
	fmt.Println("✓ kedge-agent deployed to Kubernetes")
	fmt.Printf("  Namespace: kedge-agent\n")
	fmt.Printf("  Check status: kubectl get pods -n kedge-agent\n")
	fmt.Printf("  Logs:         kubectl logs -n kedge-agent deploy/kedge-agent -f\n")
	return nil
}
