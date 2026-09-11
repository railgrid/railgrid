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
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/faroshq/faros/pkg/agent"
	"github.com/faroshq/faros/pkg/agent/tunnel"
)

// launchdInstallOptions describes a system LaunchDaemon. The daemon runs the
// normal foreground agent under the configured worker account; it does not
// invoke a second tunnel or a shell wrapper.
type launchdInstallOptions struct {
	BinaryPath      string
	HubKubeconfig   string
	HubURL          string
	Token           string
	EdgeName        string
	Type            string
	Cluster         string
	InsecureSkipTLS bool
	SvcAllowCIDRs   []string
	SvcPolicy       string
	WorkerUser      string
	WorkerHome      string
	WorkerGroup     string
	PlistPath       string
	DryRun          bool
}

type launchdWorker struct {
	username string
	group    string
	home     string
	uid      int
	gid      int
}

type launchdPlistData struct {
	Label           string
	UserName        string
	GroupName       string
	Home            string
	ProgramArgs     []string
	StandardOutPath string
	StandardErrPath string
}

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{xml .Label}}</string>
  <key>ProgramArguments</key>
  <array>
  {{- range .ProgramArgs}}
    <string>{{xml .}}</string>
  {{- end}}
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>UserName</key>
  <string>{{xml .UserName}}</string>
  <key>GroupName</key>
  <string>{{xml .GroupName}}</string>
  <key>WorkingDirectory</key>
  <string>{{xml .Home}}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>{{xml .Home}}</string>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>StandardOutPath</key>
  <string>{{xml .StandardOutPath}}</string>
  <key>StandardErrorPath</key>
  <string>{{xml .StandardErrPath}}</string>
</dict>
</plist>
`

func defaultLaunchdLabel(edgeName string) string {
	return "com.faros.agent." + edgeName
}

func defaultLaunchdPlistPath(edgeName string) string {
	return filepath.Join("/Library/LaunchDaemons", defaultLaunchdLabel(edgeName)+".plist")
}

func renderLaunchdPlist(data launchdPlistData) (string, error) {
	tmpl, err := template.New("launchd-plist").Funcs(template.FuncMap{
		"xml": func(value string) string {
			var escaped strings.Builder
			if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
				return value
			}
			return escaped.String()
		},
	}).Parse(launchdPlistTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing launchd plist template: %w", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("rendering launchd plist: %w", err)
	}
	return out.String(), nil
}

func resolveLaunchdWorker(opts launchdInstallOptions) (launchdWorker, error) {
	username := strings.TrimSpace(opts.WorkerUser)
	if username == "" {
		if os.Geteuid() == 0 {
			return launchdWorker{}, fmt.Errorf("--worker-user is required when installing a macOS LaunchDaemon as root")
		}
		current, err := user.Current()
		if err != nil {
			return launchdWorker{}, fmt.Errorf("resolving current worker account: %w", err)
		}
		username = current.Username
	}

	u, err := user.Lookup(username)
	if err != nil {
		return launchdWorker{}, fmt.Errorf("looking up worker account %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return launchdWorker{}, fmt.Errorf("invalid uid for worker account %q: %w", username, err)
	}
	if uid == 0 || username == "root" {
		return launchdWorker{}, fmt.Errorf("worker account %q must be non-root", username)
	}
	home := u.HomeDir
	if opts.WorkerHome != "" {
		home = opts.WorkerHome
	}
	if !filepath.IsAbs(home) || home == "/" {
		return launchdWorker{}, fmt.Errorf("worker home must be an absolute non-root path, got %q", home)
	}
	group := strings.TrimSpace(opts.WorkerGroup)
	if group == "" {
		group = u.Gid
	}
	gid, err := strconv.Atoi(group)
	if err != nil {
		// Allow a group name for callers that construct launchdInstallOptions
		// directly, while emitting the canonical name launchd expects in its
		// GroupName key.
		groupEntry, lookupErr := user.LookupGroup(group)
		if lookupErr != nil {
			return launchdWorker{}, fmt.Errorf("invalid worker group %q for account %q: %w", group, username, lookupErr)
		}
		gid, err = strconv.Atoi(groupEntry.Gid)
		if err != nil {
			return launchdWorker{}, fmt.Errorf("invalid gid %q for worker group %q: %w", groupEntry.Gid, group, err)
		}
		group = groupEntry.Name
	} else if groupEntry, lookupErr := user.LookupGroupId(strconv.Itoa(gid)); lookupErr == nil {
		// launchd's GroupName is a name field. Numeric values happen to work on
		// some releases but are not portable across macOS versions.
		group = groupEntry.Name
	}
	return launchdWorker{username: username, group: group, home: filepath.Clean(home), uid: uid, gid: gid}, nil
}

func validateLaunchdEdgeName(name string) error {
	if name == "" {
		return fmt.Errorf("edge name is required")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("edge name %q cannot contain path separators", name)
	}
	return nil
}

func absoluteLaunchdPath(path, field string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", field, err)
	}
	return filepath.Clean(abs), nil
}

// shellQuote formats a path or label for a command that an operator may copy
// from dry-run output. The plist itself receives each argument as an XML
// array item, so shell quoting is only needed for the human-facing launchctl
// examples.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func launchdProgramArgs(opts launchdInstallOptions) []string {
	args := []string{opts.BinaryPath, "agent", "run", "--edge-name", opts.EdgeName, "--type", "macos"}
	if opts.Cluster != "" {
		args = append(args, "--cluster", opts.Cluster)
	}
	if opts.InsecureSkipTLS {
		args = append(args, "--hub-insecure-skip-tls-verify")
	}
	for _, cidr := range opts.SvcAllowCIDRs {
		args = append(args, "--svc-allow-cidr", cidr)
	}
	if opts.SvcPolicy != "" && opts.SvcPolicy != string(tunnel.DefaultSvcPolicy) {
		args = append(args, "--svc-policy", opts.SvcPolicy)
	}
	return args
}

func launchdPaths(worker launchdWorker, edgeName string) (configPath, kubeconfigPath, logDir string) {
	configPath = agent.AgentConfigPathForHome(worker.home, edgeName)
	kubeconfigPath = agent.AgentKubeconfigPathForHome(worker.home, edgeName)
	logDir = filepath.Join(worker.home, "Library", "Logs", "Faros")
	return
}

func installLaunchdAgent(opts launchdInstallOptions) error {
	if err := validateLaunchdEdgeName(opts.EdgeName); err != nil {
		return err
	}
	if opts.Type != "" && opts.Type != "macos" {
		return fmt.Errorf("launchd installer only supports --type macos, got %q", opts.Type)
	}
	if opts.BinaryPath == "" {
		return fmt.Errorf("agent binary path is required")
	}
	if opts.Token != "" && opts.HubKubeconfig != "" {
		return fmt.Errorf("--token and --hub-kubeconfig are mutually exclusive for macOS installation")
	}
	if opts.Token != "" && strings.TrimSpace(opts.HubURL) == "" {
		return fmt.Errorf("--hub-url is required with --token")
	}
	if opts.Token == "" && opts.HubKubeconfig == "" {
		return fmt.Errorf("--token or --hub-kubeconfig is required")
	}
	if _, err := tunnel.ParseSvcAllowedCIDRs(opts.SvcAllowCIDRs); err != nil {
		return fmt.Errorf("--svc-allow-cidr: %w", err)
	}
	if opts.SvcPolicy != "" {
		if _, err := tunnel.ParseSvcPolicy(opts.SvcPolicy); err != nil {
			return fmt.Errorf("--svc-policy: %w", err)
		}
	}
	binaryPath, err := absoluteLaunchdPath(opts.BinaryPath, "agent binary path")
	if err != nil {
		return err
	}
	opts.BinaryPath = binaryPath
	if opts.HubKubeconfig != "" {
		kubeconfigPath, err := absoluteLaunchdPath(opts.HubKubeconfig, "hub kubeconfig path")
		if err != nil {
			return err
		}
		opts.HubKubeconfig = kubeconfigPath
	}
	worker, err := resolveLaunchdWorker(opts)
	if err != nil {
		return err
	}
	plistPath := opts.PlistPath
	if plistPath == "" {
		plistPath = defaultLaunchdPlistPath(opts.EdgeName)
	}
	plistPath, err = absoluteLaunchdPath(plistPath, "launchd plist path")
	if err != nil {
		return err
	}
	configPath, kubeconfigPath, logDir := launchdPaths(worker, opts.EdgeName)
	if opts.Token != "" {
		// The token is persisted in an owner-only config file and never appears
		// in the plist, launchctl arguments, or dry-run output.
	} else {
		configPath = kubeconfigPath
	}
	label := defaultLaunchdLabel(opts.EdgeName)
	plist, err := renderLaunchdPlist(launchdPlistData{
		Label:           label,
		UserName:        worker.username,
		GroupName:       worker.group,
		Home:            worker.home,
		ProgramArgs:     launchdProgramArgs(opts),
		StandardOutPath: filepath.Join(logDir, "agent-"+opts.EdgeName+".log"),
		StandardErrPath: filepath.Join(logDir, "agent-"+opts.EdgeName+".error.log"),
	})
	if err != nil {
		return err
	}

	if opts.DryRun {
		serviceTarget := "system/" + label
		fmt.Printf("--- (dry-run) Would install macOS LaunchDaemon %s ---\n", label)
		fmt.Printf("Worker: %s (uid %d), home %s\n", worker.username, worker.uid, worker.home)
		fmt.Printf("Config: %s (mode 0600; credentials omitted)\n", configPath)
		fmt.Printf("Plist:  %s (mode 0644; no credentials)\n", plistPath)
		fmt.Printf("--- plist ---\n%s", plist)
		fmt.Printf("--- Would run: launchctl bootstrap system %s; launchctl enable %s; launchctl kickstart -k %s ---\n", shellQuote(plistPath), shellQuote(serviceTarget), shellQuote(serviceTarget))
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS LaunchDaemon installation requires darwin; use --dry-run on %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("macOS LaunchDaemon installation must run as root; use sudo or --dry-run")
	}

	if err := ensureLaunchdDir(filepath.Dir(configPath), 0700, worker.uid, worker.gid); err != nil {
		return fmt.Errorf("preparing worker config directory: %w", err)
	}
	if err := ensureLaunchdDir(logDir, 0700, worker.uid, worker.gid); err != nil {
		return fmt.Errorf("preparing worker log directory: %w", err)
	}
	if opts.Token != "" {
		if err := rejectSymlink(configPath); err != nil {
			return err
		}
		if err := agent.SaveAgentConfigAt(configPath, agent.AgentConfig{
			HubURL: opts.HubURL, Token: opts.Token, Cluster: opts.Cluster,
		}); err != nil {
			return fmt.Errorf("writing worker agent config: %w", err)
		}
		if err := os.Chown(configPath, worker.uid, worker.gid); err != nil {
			return fmt.Errorf("assigning worker ownership to %s: %w", worker.username, err)
		}
		if err := os.Chmod(configPath, 0600); err != nil {
			return fmt.Errorf("protecting worker agent config: %w", err)
		}
	} else {
		if err := copyLaunchdSecret(opts.HubKubeconfig, kubeconfigPath, worker.uid, worker.gid); err != nil {
			return fmt.Errorf("installing hub kubeconfig: %w", err)
		}
	}
	if err := writeLaunchdFile(plistPath, []byte(plist), 0644, 0, 0); err != nil {
		return fmt.Errorf("writing launchd plist: %w", err)
	}

	// A stale service may already be loaded under this label. bootout is
	// intentionally best-effort so a first install proceeds normally.
	_ = exec.Command("launchctl", "bootout", "system/"+label).Run()
	for _, args := range [][]string{
		{"bootstrap", "system", plistPath},
		{"enable", "system/" + label},
		{"kickstart", "-k", "system/" + label},
	} {
		out, err := exec.Command("launchctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl %s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Printf("Agent installed and running as launchd service %s.\n", label)
	fmt.Printf("  Plist:  %s\n", plistPath)
	fmt.Printf("  Logs:   %s\n", filepath.Join(logDir, "agent-"+opts.EdgeName+".log"))
	fmt.Printf("  Uninstall: faros agent uninstall --type macos --edge-name %s\n", opts.EdgeName)
	return nil
}

func agentJoinMacOS(opts *agent.Options, workerUser, plistPath string, dryRun bool) error {
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return fmt.Errorf("resolving binary symlinks: %w", err)
	}
	return installLaunchdAgent(launchdInstallOptions{
		BinaryPath:      binaryPath,
		HubKubeconfig:   opts.HubKubeconfig,
		HubURL:          opts.HubURL,
		Token:           opts.Token,
		EdgeName:        opts.EdgeName,
		Type:            string(agent.AgentTypeMacOS),
		Cluster:         opts.Cluster,
		InsecureSkipTLS: opts.InsecureSkipTLSVerify,
		SvcAllowCIDRs:   opts.SvcAllowedCIDRs,
		SvcPolicy:       opts.SvcPolicy,
		WorkerUser:      workerUser,
		PlistPath:       plistPath,
		DryRun:          dryRun,
	})
}

func ensureLaunchdDir(path string, mode os.FileMode, uid, gid int) error {
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink at %s", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	return nil
}

// rejectSymlinkPath checks every existing component of a path before an
// installer creates or changes anything below it. This prevents a worker-owned
// symlink such as ~/.faros -> another directory from redirecting root's write.
func rejectSymlinkPath(path string) error {
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to use symlink directory %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("path component %s is not a directory", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking path component %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func writeLaunchdFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".faros-launchd-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if uid != 0 || gid != 0 {
		if err := tmp.Chown(uid, gid); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func copyLaunchdSecret(src, dst string, uid, gid int) error {
	if src == "" {
		return fmt.Errorf("source path is required")
	}
	data, err := os.ReadFile(src) //nolint:gosec // explicitly supplied credential path
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("source kubeconfig %s is empty", src)
	}
	return writeLaunchdFile(dst, data, 0600, uid, gid)
}

func uninstallLaunchdAgent(edgeName, plistPath string, dryRun bool) error {
	if err := validateLaunchdEdgeName(edgeName); err != nil {
		return err
	}
	if plistPath == "" {
		plistPath = defaultLaunchdPlistPath(edgeName)
	}
	abs, err := absoluteLaunchdPath(plistPath, "launchd plist path")
	if err != nil {
		return err
	}
	label := defaultLaunchdLabel(edgeName)
	if dryRun {
		fmt.Printf("--- (dry-run) Would bootout system/%s and remove %s ---\n", label, abs)
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS LaunchDaemon uninstall requires darwin; use --dry-run on %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("macOS LaunchDaemon uninstall must run as root")
	}
	if out, err := exec.Command("launchctl", "bootout", "system/"+label).CombinedOutput(); err != nil {
		// launchctl exits non-zero when the service was already unloaded; removal
		// remains safe, so report the detail and continue.
		fmt.Fprintf(os.Stderr, "Warning: launchctl bootout %s: %v\n%s", label, err, out)
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", abs, err)
	}
	fmt.Printf("LaunchDaemon %s uninstalled.\n", label)
	return nil
}
