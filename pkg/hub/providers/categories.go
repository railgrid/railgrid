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

package providers

// Category describes how the portal renders a `spec.category` value in
// the nav and on the providers catalog page. Each entry binds a name
// (matched case-sensitively against CatalogEntry.spec.category) to a
// display label, a lucide-vue-next icon component name, and a sort hint.
//
// Categories not in this list still render — a CatalogEntry can put
// itself in an ad-hoc category like "Observability" — but they fall back
// to the default Puzzle icon and sort after the named ones.
//
// To add a new built-in category: append an entry here, pick a Lucide
// icon name from https://lucide.dev/icons/ (the portal maps the string
// to an imported component in lib/categoryIcons.ts). No portal-side
// change is required unless the icon isn't already imported.
type Category struct {
	// Name is the case-sensitive value that must match
	// CatalogEntry.spec.category for a provider to be grouped here.
	Name string
	// Icon is the lucide-vue-next component name (e.g. "Server",
	// "Sparkles"). Used by the portal as a string lookup; the actual Vue
	// component is resolved client-side.
	Icon string
	// Order is the sort key (ascending). Lower numbers appear higher in
	// the nav. Use multiples of 10 to leave room for future entries.
	Order int
}

// Categories is the canonical list of built-in nav categories. Order
// matters for the default sort; alphabetical-by-display-name kicks in
// only as a tiebreaker between equal Order values.
var Categories = []Category{
	{Name: "Edges", Icon: "Server", Order: 10},
	{Name: "AI", Icon: "Sparkles", Order: 20},
	{Name: "Demo", Icon: "Package", Order: 90},
}

// CategoryByName returns the Category record for name, or zero value
// (and ok=false) when unknown. Ad-hoc categories used by third-party
// CatalogEntries surface here with no record.
func CategoryByName(name string) (Category, bool) {
	for _, c := range Categories {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}
