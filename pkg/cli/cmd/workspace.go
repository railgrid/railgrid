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
	"os"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	workspacecmd "github.com/kcp-dev/cli/pkg/workspace/cmd"
)

func newWorkspaceCommand() *cobra.Command {
	wsCmd, err := workspacecmd.New(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		// This only fails if the create subcommand can't be built, which
		// shouldn't happen. Panic rather than silently returning nil.
		panic(err)
	}
	return wsCmd
}
