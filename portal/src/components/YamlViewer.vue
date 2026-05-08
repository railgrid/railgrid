<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronRight, ChevronDown } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    source: string
    autoCollapseKeys?: string[]
  }>(),
  {
    autoCollapseKeys: () => ['managedFields'],
  },
)

interface ParsedLine {
  raw: string
  indent: number
  isFoldStart: boolean
  key?: string
  childEnd: number
}

const indentOf = (line: string) => {
  let n = 0
  while (n < line.length && line[n] === ' ') n++
  return n
}

const parsed = computed<ParsedLine[]>(() => {
  const raw = (props.source ?? '').split('\n')
  const result: ParsedLine[] = raw.map((l) => ({
    raw: l,
    indent: indentOf(l),
    isFoldStart: false,
    childEnd: -1,
  }))

  const keyOnlyRegex = /^(\s*)(?:- )?([^\s][^:]*?):\s*(?:#.*)?$/

  for (let i = 0; i < raw.length; i++) {
    const line = raw[i]
    if (line.trim() === '' || line.trim().startsWith('#')) continue
    const m = line.match(keyOnlyRegex)
    if (!m) continue
    const keyIndent = result[i].indent
    const key = m[2]

    let end = i + 1
    while (end < raw.length) {
      const next = raw[end]
      if (next.trim() === '') {
        end++
        continue
      }
      const nextIndent = indentOf(next)
      if (nextIndent < keyIndent) break
      if (nextIndent === keyIndent) {
        const trimmed = next.trimStart()
        if (!trimmed.startsWith('-')) break
      }
      end++
    }

    while (end > i + 1 && raw[end - 1].trim() === '') end--

    if (end > i + 1) {
      result[i].isFoldStart = true
      result[i].key = key
      result[i].childEnd = end
    }
  }

  return result
})

const collapsed = ref<Set<number>>(new Set())
const initSignature = ref<string>('')

watch(
  parsed,
  (lines) => {
    const sig = `${lines.length}:${props.source.length}`
    if (sig === initSignature.value) return
    initSignature.value = sig
    const next = new Set<number>()
    lines.forEach((l, i) => {
      if (l.isFoldStart && props.autoCollapseKeys.includes(l.key ?? '')) {
        next.add(i)
      }
    })
    collapsed.value = next
  },
  { immediate: true },
)

const toggle = (i: number) => {
  const s = new Set(collapsed.value)
  if (s.has(i)) s.delete(i)
  else s.add(i)
  collapsed.value = s
}

const visible = computed(() => {
  const out: { idx: number; line: ParsedLine }[] = []
  let skipUntil = -1
  for (let i = 0; i < parsed.value.length; i++) {
    if (i < skipUntil) continue
    const l = parsed.value[i]
    out.push({ idx: i, line: l })
    if (l.isFoldStart && collapsed.value.has(i)) {
      skipUntil = l.childEnd
    }
  }
  return out
})

const expandAll = () => {
  collapsed.value = new Set()
}

const collapseAll = () => {
  const next = new Set<number>()
  parsed.value.forEach((l, i) => {
    if (l.isFoldStart) next.add(i)
  })
  collapsed.value = next
}
</script>

<template>
  <div class="relative">
    <div class="absolute right-2 top-2 z-10 flex gap-1">
      <button
        class="rounded-md border border-border-subtle bg-surface-raised/80 px-2 py-0.5 text-[10px] font-medium text-text-muted backdrop-blur transition-colors hover:border-accent/30 hover:text-text-primary"
        @click="expandAll"
      >
        Expand all
      </button>
      <button
        class="rounded-md border border-border-subtle bg-surface-raised/80 px-2 py-0.5 text-[10px] font-medium text-text-muted backdrop-blur transition-colors hover:border-accent/30 hover:text-text-primary"
        @click="collapseAll"
      >
        Collapse all
      </button>
    </div>
    <div class="max-h-[500px] overflow-auto rounded-2xl border border-border-subtle bg-surface-overlay/60 py-3 pl-2 pr-5 font-mono text-[11px] leading-relaxed text-text-secondary backdrop-blur">
      <div
        v-for="item in visible"
        :key="item.idx"
        class="group flex min-w-0 items-start whitespace-pre"
      >
        <button
          v-if="item.line.isFoldStart"
          class="mr-0.5 inline-flex h-[1.45em] w-4 shrink-0 items-center justify-center text-text-muted/50 transition-colors hover:text-accent"
          @click="toggle(item.idx)"
          :title="collapsed.has(item.idx) ? 'Expand' : 'Collapse'"
        >
          <ChevronRight v-if="collapsed.has(item.idx)" class="h-3 w-3" :stroke-width="2.25" />
          <ChevronDown v-else class="h-3 w-3" :stroke-width="2.25" />
        </button>
        <span v-else class="mr-0.5 inline-block h-[1.45em] w-4 shrink-0" />
        <span class="min-w-0">{{ item.line.raw }}</span>
        <span
          v-if="item.line.isFoldStart && collapsed.has(item.idx)"
          class="ml-2 shrink-0 rounded bg-surface-overlay px-1.5 text-[10px] text-text-muted/70"
        >
          {{ item.line.childEnd - item.idx - 1 }} lines
        </span>
      </div>
    </div>
  </div>
</template>
