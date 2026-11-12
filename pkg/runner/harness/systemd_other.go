//go:build !linux

// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package harness

import "errors"

func userSystemdEnvironment() (map[string]string, error) {
	return nil, errors.New("userSystemd is supported only on Linux")
}
