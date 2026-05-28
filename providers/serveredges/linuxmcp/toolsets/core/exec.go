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

package core

import (
	"context"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

// execShell is a thin re-export of sshexec.Run that keeps the core toolset's
// internal call sites short.  The shared helper centralises timeout / output
// cap / exit-code handling.
func execShell(ctx context.Context, p *linuxmcp.Provider, target, cmd string) (sshexec.Result, error) {
	return sshexec.Run(ctx, p, target, cmd)
}
