/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import "errors"

// ErrCredentialsMissing means the Connection's referenced Secret was not found
// in the tenant workspace — the tenant has not finished onboarding (pasting a
// PAT) yet.
var ErrCredentialsMissing = errors.New("connection credential secret not found in tenant workspace")

// ErrAPIBindingMissing means the read got a 403 — the tenant has not accepted
// this provider's secrets permission claim (Enable flow incomplete).
var ErrAPIBindingMissing = errors.New("tenant has no APIBinding (or secrets claim) to this provider's APIExport")
