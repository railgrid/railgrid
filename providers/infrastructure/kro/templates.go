// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import "errors"

// ErrTemplateNotFound is returned when no template with the requested
// name (and version, when one was pinned) exists in the catalog.
var ErrTemplateNotFound = errors.New("template not found")
