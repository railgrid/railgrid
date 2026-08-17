// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package install

import (
	"fmt"
	"net/url"
	"strings"
)

// RetargetHostToWorkspace rewrites a kcp host URL so it terminates at
// /clusters/<workspacePath>. Idempotent — an existing /clusters/… segment
// is replaced rather than appended. Exported so the operator's rest.Config
// retarget and the init command share one implementation.
func RetargetHostToWorkspace(host, workspacePath string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse host %q: %w", host, err)
	}
	if idx := strings.Index(u.Path, "/clusters/"); idx >= 0 {
		// Already cluster-scoped. Leave a specific workspace (cluster ID, e.g.
		// the admin-portal-issued provider kubeconfig, or a workspace path)
		// untouched — only a "root"-scoped host is retargeted. Front proxies that
		// route by cluster ID 404 on a path form, so rewriting an already-scoped
		// kubeconfig breaks access.
		seg := strings.SplitN(strings.TrimPrefix(u.Path[idx:], "/clusters/"), "/", 2)[0]
		if seg != "" && seg != "root" {
			return host, nil
		}
		u.Path = u.Path[:idx]
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/clusters/" + workspacePath
	return u.String(), nil
}
