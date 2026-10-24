// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0

package backend

import "errors"

// ErrToolResultReferenceCallerAuthorizationUnavailable identifies a reader
// refusal caused by the absence of current caller credentials. Adapters may
// return it when a protected reference cannot be reopened in the current
// execution context; the engine exposes only this safe, fixed reason.
var ErrToolResultReferenceCallerAuthorizationUnavailable = errors.New("current caller authorization is unavailable")
