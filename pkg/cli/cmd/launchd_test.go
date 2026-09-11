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
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidateLaunchdEdgeName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "ordinary name with x", value: "macbook-01x"},
		{name: "ordinary name with zero", value: "macbook-010"},
		{name: "empty", value: "", wantErr: true},
		{name: "dot", value: ".", wantErr: true},
		{name: "dot dot", value: "..", wantErr: true},
		{name: "slash", value: "macbook/01", wantErr: true},
		{name: "backslash", value: "macbook\\01", wantErr: true},
		{name: "nul", value: "macbook-01" + string(rune(0)), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLaunchdEdgeName(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLaunchdEdgeName(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func nonRootLaunchdUser(t *testing.T) *user.User {
	t.Helper()
	for _, name := range []string{"nobody", "daemon"} {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(u.Uid)
		if err == nil && uid > 0 {
			return u
		}
	}
	t.Skip("no non-root test account is available")
	return nil
}

func captureLaunchdStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	callErr := fn()
	_ = w.Close()
	os.Stdout = previous
	data, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data), callErr
}

func TestResolveLaunchdWorkerRejectsRootAndUnsafeHomes(t *testing.T) {
	if _, err := resolveLaunchdWorker(launchdInstallOptions{WorkerUser: "root"}); err == nil {
		t.Fatal("root worker account was accepted")
	}
	u := nonRootLaunchdUser(t)
	for _, home := range []string{"/", "relative/home"} {
		t.Run(home, func(t *testing.T) {
			if _, err := resolveLaunchdWorker(launchdInstallOptions{
				WorkerUser: u.Username,
				WorkerHome: home,
			}); err == nil {
				t.Fatalf("worker home %q was accepted", home)
			}
		})
	}

	home := t.TempDir()
	worker, err := resolveLaunchdWorker(launchdInstallOptions{
		WorkerUser: u.Username,
		WorkerHome: home,
	})
	if err != nil {
		t.Fatalf("valid worker was rejected: %v", err)
	}
	if worker.username != u.Username || worker.home != home || worker.uid == 0 {
		t.Fatalf("resolved worker = %+v, want non-root %q at %q", worker, u.Username, home)
	}
}

func TestInstallLaunchdDryRunDoesNotWriteOrLeakToken(t *testing.T) {
	u := nonRootLaunchdUser(t)
	workerHome := filepath.Join(t.TempDir(), "worker home")
	plistPath := filepath.Join(t.TempDir(), "launchd", "agent.plist")
	const token = "join-token-must-not-appear"

	output, err := captureLaunchdStdout(t, func() error {
		return installLaunchdAgent(launchdInstallOptions{
			BinaryPath:    "/usr/local/bin/faros",
			HubURL:        "https://hub.example/clusters/root:faros:tenant",
			Token:         token,
			EdgeName:      "macbook-01",
			Type:          "macos",
			Cluster:       "root:faros:tenant",
			WorkerUser:    u.Username,
			WorkerHome:    workerHome,
			PlistPath:     plistPath,
			SvcAllowCIDRs: []string{"192.168.1.0/24"},
			SvcPolicy:     "enforce",
			DryRun:        true,
		})
	})
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if strings.Contains(output, token) || strings.Contains(output, "--token") {
		t.Fatalf("dry-run leaked bootstrap credentials:\n%s", output)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run plist stat error = %v, want no file", err)
	}
	if _, err := os.Stat(filepath.Join(workerHome, ".faros")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created credential directory: %v", err)
	}
	for _, want := range []string{"com.faros.agent.macbook-01", "root:faros:tenant", "--svc-policy", "enforce"} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, output)
		}
	}
}

func TestRenderLaunchdPlistEscapesXML(t *testing.T) {
	plist, err := renderLaunchdPlist(launchdPlistData{
		Label:           `com.faros.agent.edge&<>",`,
		UserName:        `worker&<>`,
		GroupName:       `staff&<>`,
		Home:            `/Users/worker&<>`,
		ProgramArgs:     []string{"/usr/local/bin/faros", `arg&<>"'`},
		StandardOutPath: `/Users/worker&<>/agent.log`,
		StandardErrPath: `/Users/worker&<>/agent.error.log`,
	})
	if err != nil {
		t.Fatalf("renderLaunchdPlist: %v", err)
	}
	var document struct{}
	if err := xml.Unmarshal([]byte(plist), &document); err != nil {
		t.Fatalf("rendered plist is not XML: %v\n%s", err, plist)
	}
	for _, escaped := range []string{"&amp;", "&lt;", "&gt;"} {
		if !strings.Contains(plist, escaped) {
			t.Errorf("rendered plist lacks XML escape %q:\n%s", escaped, plist)
		}
	}
}

func TestWriteLaunchdFileRefusesSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "plist")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeLaunchdFile(link, []byte("overwrite"), 0644, 0, 0); err == nil {
		t.Fatal("writeLaunchdFile followed a symlink destination")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed to %q", data)
	}
}
