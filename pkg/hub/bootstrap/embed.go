package bootstrap

import "embed"

//go:embed crds/*.yaml
var crdFS embed.FS
