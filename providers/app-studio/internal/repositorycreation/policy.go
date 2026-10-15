/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repositorycreation shares automatic creation retry policy between the
// project reconciler and its user-facing repository view.
package repositorycreation

const MaxRetries = 5
const AttemptsAnnotation = "ai.railgrid.ai/repository-name-attempts"
const LimitMessage = "Repository creation stopped after 5 automatic name retries. Review the Git connection and choose Create new repository in Git settings to try again, or explicitly import an existing repository."
