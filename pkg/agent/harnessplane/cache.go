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

package harnessplane

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/railgrid/railgrid/pkg/util/safeio"
)

// cacheRecord is the on-disk form of the last setting the agent OBSERVED on its
// edge object. It exists for one reason: a machine that boots while the hub is
// unreachable must run what it was last told rather than what its install flags
// said, and rather than nothing at all.
type cacheRecord struct {
	Mode      Mode     `json:"mode"`
	Enabled   []string `json:"enabled,omitempty"`
	UpdatedAt string   `json:"updatedAt"`
}

// CachePath is <agentHome>/.railgrid/agent-<edge>.harness.json. It lives under
// the AGENT's home, not the runner account's: it is the agent's memory of a hub
// decision, and the account that executes untrusted code has no business being
// able to rewrite it.
func CachePath(agentHome, edgeName string) string {
	return filepath.Join(agentHome, ".railgrid", "agent-"+edgeName+".harness.json")
}

// LoadCache reads the cached setting. The second return is false when no cache
// exists yet, which is the only case in which a flag may seed one.
func LoadCache(path string) (Setting, bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is built from the agent's own home and edge name
	if os.IsNotExist(err) {
		return Setting{}, false, nil
	}
	if err != nil {
		return Setting{}, false, err
	}
	var record cacheRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return Setting{}, false, fmt.Errorf("decoding %s: %w", path, err)
	}
	setting := Setting{Mode: record.Mode, Enabled: record.Enabled}
	if err := setting.Validate(); err != nil {
		return Setting{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return setting, true, nil
}

// WriteCache records the effective setting. It is called after every OBSERVED
// change, so the file always describes the hub's last word.
func WriteCache(path string, setting Setting) error {
	if err := safeio.EnsureDir(filepath.Dir(path), 0700, 0, 0); err != nil {
		return err
	}
	body, err := json.Marshal(cacheRecord{
		Mode:      setting.mode(),
		Enabled:   setting.Enabled,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return safeio.WriteFile(path, append(body, '\n'), 0600, 0, 0)
}

// SeedCache writes setting ONLY when no cache exists, and reports whether it
// wrote. This is the whole of what an install flag may do: it cannot override a
// setting the agent has already observed on its edge, because the observation
// is what wrote the file it refuses to touch.
func SeedCache(path string, setting Setting) (bool, error) {
	// Lstat rather than LoadCache: a cache that exists but cannot be parsed is
	// still a cache. A flag must not quietly take over because a file was
	// corrupt — the next observation will rewrite it correctly.
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := WriteCache(path, setting); err != nil {
		return false, err
	}
	return true, nil
}
