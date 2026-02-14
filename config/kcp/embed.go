package kcp

import "embed"

//go:embed *.yaml
var workspaceFS embed.FS

// WorkspaceFS returns the embedded filesystem containing workspace YAML files.
func WorkspaceFS() embed.FS {
	return workspaceFS
}
