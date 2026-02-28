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

// Package framework provides shared test infrastructure for kedge e2e tests.
package framework

import (
	"flag"
	"time"
)

var (
	// SSHKeepaliveDuration is how long the long-lived SSH connection test holds the
	// session open before asserting liveness. Default 5m; bump to 10m+ locally.
	SSHKeepaliveDuration time.Duration

	// KeepClusters controls whether kind clusters are deleted after the test run.
	// Default is false (clusters are deleted). Set --keep-clusters to retain them
	// for debugging failures.
	KeepClusters bool

	// KedgeBin is the path to the kedge binary under test.
	KedgeBin string

	// DevToken is the static auth token used in standalone (non-OIDC) test suites.
	DevToken string
)

func init() {
	flag.BoolVar(&KeepClusters, "keep-clusters", false, "Keep kind clusters after test run (useful for debugging failures)")
	flag.StringVar(&KedgeBin, "kedge-bin", "bin/kedge", "Path to the kedge CLI binary")
	flag.StringVar(&DevToken, "dev-token", "dev-token", "Static auth token for non-OIDC test suites")
	flag.DurationVar(&SSHKeepaliveDuration, "ssh-keepalive-duration", 30*time.Second, "How long to hold the long-lived SSH session open in the keepalive test")
}
