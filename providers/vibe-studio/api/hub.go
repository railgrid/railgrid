// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"os"
	"strings"
)

// hubBaseURL is the hub this provider reaches tenant runtimes through.
func hubBaseURL() string { return strings.TrimRight(os.Getenv("FAROS_HUB_URL"), "/") }

// hubInsecure relaxes TLS for the dev hub's self-signed cert (same knob the
// heartbeat uses).
func hubInsecure() bool { return os.Getenv("FAROS_HUB_INSECURE") == "true" }
