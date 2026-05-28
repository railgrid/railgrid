// categoryIcons maps the Lucide icon names the hub returns via
// /api/providers.categories[].icon to the actual lucide-vue-next
// components. The hub is authoritative on which categories exist (see
// pkg/hub/providers/categories.go); the portal just renders the icon it
// names.
//
// To support a new Lucide icon: add the import + entry below. Anything
// not in this map falls back to Puzzle (handled by the caller).

import {
  Bot,
  Brain,
  Database,
  Layers,
  Package,
  Puzzle,
  Server,
  Settings,
  Sparkles,
  type LucideIcon,
} from 'lucide-vue-next'

export const categoryIcons: Record<string, LucideIcon> = {
  Bot,
  Brain,
  Database,
  Layers,
  Package,
  Puzzle,
  Server,
  Settings,
  Sparkles,
}

export const fallbackCategoryIcon: LucideIcon = Puzzle
