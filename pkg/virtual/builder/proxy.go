package builder

import (
	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"k8s.io/klog/v2"
)

// virtualWorkspaces holds state and dependencies for all virtual workspaces.
type virtualWorkspaces struct {
	rootPathPrefix string
	connManager    *connman.ConnectionManager
	logger         klog.Logger
}
