// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import "github.com/faroshq/provider-sdk/modelcatalog"

type ModelInfo = modelcatalog.ModelInfo

const DefaultContextWindow = modelcatalog.DefaultContextWindow

func Catalog() []ModelInfo                       { return modelcatalog.Catalog() }
func LookupModel(model string) (ModelInfo, bool) { return modelcatalog.LookupModel(model) }
func CostMicros(model string, inTokens, outTokens int64) int64 {
	return modelcatalog.CostMicros(model, inTokens, outTokens)
}
func ContextWindowFor(model string) int { return modelcatalog.ContextWindowFor(model) }
func EstimateTokens(s string) int       { return modelcatalog.EstimateTokens(s) }
