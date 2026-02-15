package site

import "time"

const (
	// HeartbeatTimeout is the duration after which a site is considered disconnected.
	HeartbeatTimeout = 5 * time.Minute
	// GCTimeout is the duration after which a disconnected site is garbage collected.
	GCTimeout = 24 * time.Hour
)
