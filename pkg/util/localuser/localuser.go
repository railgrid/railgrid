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

// Package localuser resolves the local account a privileged installer or
// supervisor drops privileges to. It exists because two callers need the same
// answer and the same refusals: the macOS LaunchDaemon installer
// (pkg/cli/cmd/launchd.go) and the edge add-on supervisor, which launches a
// code-execution child from a root agent.
//
// The refusals are the point. uid 0 is rejected, and so is a home directory
// that is relative or is "/" — a state directory created under "/" would scatter
// owner-only files across the filesystem root.
package localuser

import (
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Account is a resolved non-root local account.
type Account struct {
	// Username as it appears in the account database.
	Username string
	// Group is the canonical PRIMARY GROUP NAME, not a gid string: launchd's
	// GroupName key is a name field, and numeric values are not portable across
	// macOS releases.
	Group string
	// Home is an absolute, cleaned home directory that is not "/".
	Home string
	// UID and GID of the account.
	UID int
	GID int
}

// Resolve looks up username and applies the shared refusals. homeOverride and
// groupOverride are optional; an empty value takes the account database's.
// groupOverride may be a gid or a group name and is normalized to a name.
func Resolve(username, homeOverride, groupOverride string) (Account, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return Account{}, fmt.Errorf("account name is required")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return Account{}, fmt.Errorf("looking up worker account %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return Account{}, fmt.Errorf("invalid uid for worker account %q: %w", username, err)
	}
	if uid == 0 || username == "root" {
		return Account{}, fmt.Errorf("worker account %q must be non-root", username)
	}
	home := u.HomeDir
	if homeOverride != "" {
		home = homeOverride
	}
	if !filepath.IsAbs(home) || home == "/" {
		return Account{}, fmt.Errorf("worker home must be an absolute non-root path, got %q", home)
	}
	group := strings.TrimSpace(groupOverride)
	if group == "" {
		group = u.Gid
	}
	gid, err := strconv.Atoi(group)
	if err != nil {
		// Allow a group name for callers that pass one directly, while emitting
		// the canonical name.
		groupEntry, lookupErr := user.LookupGroup(group)
		if lookupErr != nil {
			return Account{}, fmt.Errorf("invalid worker group %q for account %q: %w", group, username, lookupErr)
		}
		gid, err = strconv.Atoi(groupEntry.Gid)
		if err != nil {
			return Account{}, fmt.Errorf("invalid gid %q for worker group %q: %w", groupEntry.Gid, group, err)
		}
		group = groupEntry.Name
	} else if groupEntry, lookupErr := user.LookupGroupId(strconv.Itoa(gid)); lookupErr == nil {
		group = groupEntry.Name
	}
	return Account{Username: username, Group: group, Home: filepath.Clean(home), UID: uid, GID: gid}, nil
}

// Current resolves the account the calling process already runs as. It applies
// the same refusals, so a root process never gets an Account back.
func Current() (Account, error) {
	u, err := user.Current()
	if err != nil {
		return Account{}, fmt.Errorf("resolving current account: %w", err)
	}
	return Resolve(u.Username, "", "")
}
