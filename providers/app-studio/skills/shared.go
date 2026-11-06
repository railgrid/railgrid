/*
Copyright 2026 The Railgrid Authors.

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

// Package skills contains the framework-neutral App Studio skill catalog.
//
// The package deliberately models only bounded, declarative skill metadata and
// content. Eino-specific authority fields (context, agent, and model) are
// rejected while loading instead of being interpreted by this package.
package skills

import sdk "github.com/railgrid/provider-sdk/skills"

// Compatibility aliases retain App Studio’s provider API while sharing the implementation.
type (
	Scope                 = sdk.Scope
	Limits                = sdk.Limits
	Warning               = sdk.Warning
	ResourceRef           = sdk.ResourceRef
	Package               = sdk.Package
	PackageList           = sdk.PackageList
	ResourceReadOptions   = sdk.ResourceReadOptions
	ResourceReadResult    = sdk.ResourceReadResult
	Resource              = sdk.Resource
	Entry                 = sdk.Entry
	Snapshot              = sdk.Snapshot
	Source                = sdk.Source
	Catalog               = sdk.Catalog
	CatalogOptions        = sdk.CatalogOptions
	ParsedSkill           = sdk.ParsedSkill
	ProviderSkillPackage  = sdk.ProviderSkillPackage
	ProviderSkillResource = sdk.ProviderSkillResource
	Activation            = sdk.Activation
)

const (
	ScopeSystem                                  = sdk.ScopeSystem
	ScopeProject                                 = sdk.ScopeProject
	DefaultMaxPackages                           = sdk.DefaultMaxPackages
	DefaultMaxWarnings                           = sdk.DefaultMaxWarnings
	DefaultMaxSkillBytes                         = sdk.DefaultMaxSkillBytes
	DefaultMaxAggregateBytes                     = sdk.DefaultMaxAggregateBytes
	DefaultMaxResourceBytes                      = sdk.DefaultMaxResourceBytes
	DefaultMaxResourceCount                      = sdk.DefaultMaxResourceCount
	DefaultMaxResourceRead                       = sdk.DefaultMaxResourceRead
	DefaultMaxProjectFileCount                   = sdk.DefaultMaxProjectFileCount
	HardMaxPackages                              = sdk.HardMaxPackages
	HardMaxWarnings                              = sdk.HardMaxWarnings
	HardMaxSkillBytes                            = sdk.HardMaxSkillBytes
	HardMaxAggregateBytes                        = sdk.HardMaxAggregateBytes
	HardMaxResourceBytes                         = sdk.HardMaxResourceBytes
	HardMaxResourceCount                         = sdk.HardMaxResourceCount
	HardMaxResourceRead                          = sdk.HardMaxResourceRead
	HardMaxProjectFileCount                      = sdk.HardMaxProjectFileCount
	ProviderSkillSourceMaxProviderAggregateBytes = sdk.ProviderSkillSourceMaxProviderAggregateBytes
	ProviderSkillSourceMaxAggregateBytes         = sdk.ProviderSkillSourceMaxAggregateBytes
)

var (
	DefaultLimits                     = sdk.DefaultLimits
	NewCatalog                        = sdk.NewCatalog
	ValidatePackagePath               = sdk.ValidatePackagePath
	ValidateResourcePath              = sdk.ValidateResourcePath
	Build                             = sdk.Build
	LoadSnapshot                      = sdk.LoadSnapshot
	ParseSkill                        = sdk.ParseSkill
	Parse                             = sdk.Parse
	NewFilesystemSource               = sdk.NewFilesystemSource
	NewBundledSource                  = sdk.NewBundledSource
	NewProviderSkillSource            = sdk.NewProviderSkillSource
	NewProviderSkillUnavailableSource = sdk.NewProviderSkillUnavailableSource
	ProviderSkillPackageDigest        = sdk.ProviderSkillPackageDigest
	NewActivationSource               = sdk.NewActivationSource
	ErrSkillNotFound                  = sdk.ErrSkillNotFound
	ErrResourceNotFound               = sdk.ErrResourceNotFound
)
