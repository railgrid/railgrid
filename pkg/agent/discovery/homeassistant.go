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

package discovery

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	haType        = "home-assistant"
	haDefaultPort = 8123
	haImagePrefix = "ghcr.io/home-assistant/home-assistant"
	haObserverURL = "http://127.0.0.1:4357/"
)

// homeAssistantDetector finds a Home Assistant instance on the host. It probes
// cheapest-first: a loopback HTTP fingerprint on the default port, then a
// container check (which also yields the image tag and non-default ports),
// then systemd units, then the HAOS/Supervised observer.
type homeAssistantDetector struct{}

func (d *homeAssistantDetector) Name() string { return haType }

func (d *homeAssistantDetector) Detect(ctx context.Context) (*DiscoveredService, bool) {
	svc := &DiscoveredService{
		Name:   haType,
		Type:   haType,
		Scheme: "http",
		Port:   haDefaultPort,
	}

	// 1. Container check first — it upgrades metadata (version, port, install
	//    type) and works even when HA binds a non-default port.
	found := d.detectContainer(ctx, svc)

	// 2. Port fingerprint on whatever port we have (default or container-derived).
	if d.probeAPI(ctx, svc.Port) {
		found = true
	}

	// 3. systemd (core venv installs) — only meaningful if not already a container.
	if !found && d.detectSystemd(ctx) {
		svc.InstallType = "core"
		found = true
	}

	// 4. HAOS / Supervised observer.
	if d.detectObserver(ctx) {
		if svc.InstallType == "" {
			svc.InstallType = "haos"
		}
		found = true
	}

	if !found {
		return nil, false
	}
	return svc, true
}

// probeAPI hits GET /api/ on the loopback port. HA answers 401 (auth required)
// or 200 {"message":"API running."} — either is a positive fingerprint.
func (d *homeAssistantDetector) probeAPI(ctx context.Context, port int32) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(int(port)) + "/api/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck
	// 401 (unauthorized) and 200 (running, no auth) both indicate HA's API.
	return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK
}

// detectContainer looks for a running HA container via docker (then podman) and
// fills version/port/installType. Returns true when the image is present.
func (d *homeAssistantDetector) detectContainer(ctx context.Context, svc *DiscoveredService) bool {
	for _, engine := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(engine); err != nil {
			continue
		}
		out, err := runCmd(ctx, engine, "ps", "--no-trunc", "--format", "{{.Image}}|{{.Ports}}")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			image, ports, ok := strings.Cut(line, "|")
			if !ok || !strings.HasPrefix(image, haImagePrefix) {
				continue
			}
			svc.InstallType = "container"
			if _, tag, ok := strings.Cut(image, ":"); ok && tag != "" {
				svc.Version = tag
			}
			if p := parseHostPort(ports, haDefaultPort); p != 0 {
				svc.Port = p
			}
			return true
		}
	}
	return false
}

// detectSystemd returns true if an HA systemd service unit is loaded.
func (d *homeAssistantDetector) detectSystemd(ctx context.Context) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	out, err := runCmd(ctx, "systemctl", "list-units", "--type=service",
		"--all", "--no-legend", "--plain", "home-assistant*", "hass*")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// detectObserver returns true if the HAOS/Supervised observer answers on :4357.
func (d *homeAssistantDetector) detectObserver(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, haObserverURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode < 500
}

// runCmd runs an external command with a hard timeout and returns stdout.
func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseHostPort extracts the published host port that maps to targetPort from a
// docker ports string like "0.0.0.0:8123->8123/tcp, :::8123->8123/tcp". Returns
// 0 when no mapping is found (e.g. host networking).
func parseHostPort(ports string, targetPort int32) int32 {
	suffix := "->" + strconv.Itoa(int(targetPort)) + "/"
	for _, mapping := range strings.Split(ports, ",") {
		mapping = strings.TrimSpace(mapping)
		idx := strings.Index(mapping, suffix)
		if idx < 0 {
			continue
		}
		left := mapping[:idx] // "0.0.0.0:8123" or ":::8123"
		_, hostPort, ok := lastColonSplit(left)
		if !ok {
			continue
		}
		if p, err := strconv.Atoi(hostPort); err == nil {
			return int32(p)
		}
	}
	return 0
}

// lastColonSplit splits on the final colon, tolerating IPv6 host forms.
func lastColonSplit(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
