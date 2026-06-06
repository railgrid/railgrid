/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// Tiny shared JSON helpers so controller.go and apiexport.go don't
// each maintain their own import block + alias.

import "encoding/json"

func crdsJSONMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func crdsJSONUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
