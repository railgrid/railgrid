// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import "strings"

// explainGitError turns a git-host refusal into something the person reading
// the Status tab can act on.
//
// The one that actually happens: projects are seeded with the scaffold's build
// workflow under `.github/workflows/`, and a connection without the `workflow`
// OAuth scope has the *entire* commit rejected — not just that file. GitHub
// words it as "refusing to allow an OAuth App to create or update workflow …
// without `workflow` scope", which reads like a bug in the platform unless you
// already know what to do about it.
func explainGitError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "workflow") && strings.Contains(low, "scope") {
		return "your Git connection is not allowed to write GitHub Actions workflows, and projects are seeded with a build workflow. " +
			"Reconnect the Git account in the Code provider, granting the `workflow` scope, then this will retry on its own. " +
			"(git host said: " + msg + ")"
	}
	return msg
}
