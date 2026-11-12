<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, BookOpen, ChevronRight, Code2, Hexagon, Search } from 'lucide-vue-next'
import { useThemeStore } from '@/stores/theme'
import catalog from 'virtual:design-review'
import ReviewStage from './ReviewStage.vue'

const groups = [
  ['foundations', 'Foundations'], ['components', 'Components'], ['patterns', 'Patterns'],
  ['ai', 'AI experience'], ['content', 'Content'], ['accessibility', 'Accessibility'], ['quality', 'Quality'],
]
const theme = useThemeStore()
const query = ref('')
const viewSize = ref('full')
const mobileIndex = ref(false)
const locationState = ref(new URLSearchParams(location.search))
const title = ref(null)
const standaloneFrame = new URLSearchParams(location.search).get('frame') === '1'
const previewTheme = new URLSearchParams(location.search).get('theme')
if (standaloneFrame && ['dark', 'light'].includes(previewTheme)) {
  document.documentElement.classList.remove('dark', 'light')
  document.documentElement.classList.add(previewTheme)
  document.documentElement.style.colorScheme = previewTheme
}
const requested = key => locationState.value.get(key)
const foundationOrder = ['system', 'principles', 'colors', 'geometry', 'typography', 'recipes', 'theming', 'iconography', 'provider-integration']
const entries = groups.flatMap(([key]) => {
  const items = catalog.entries.filter(doc => doc.id.startsWith('design.' + key + '.'))
  return key === 'foundations' ? items.toSorted((a, b) => foundationOrder.indexOf(a.id.split('.').at(-1)) - foundationOrder.indexOf(b.id.split('.').at(-1))) : items
})
const entry = computed(() => entries.find(doc => doc.id === requested('entry')) ?? entries.find(doc => doc.id === 'design.foundations.system'))
const entryIndex = computed(() => entries.indexOf(entry.value))
const group = computed(() => groups.find(([key]) => entry.value.id.startsWith('design.' + key + '.'))[1])
const matches = computed(() => groups.map(([key, label]) => ({
  key, label, documents: entries.filter(doc => doc.id.startsWith('design.' + key + '.') &&
    query.value.toLowerCase().trim().split(/\s+/).every(term => (
      [doc.title, doc.id, doc.review.title, doc.review.tryThis, ...doc.sources.map(item => item.path), ...doc.sections.map(item => item.title)].join(' ').toLowerCase().includes(term)
    ))),
})).filter(item => item.documents.length))
const path = computed(() => requested('source'))
const file = computed(() => Object.hasOwn(catalog.sources, path.value) ? catalog.sources[path.value] : null)
const line = computed(() => Math.max(1, Math.min(file.value?.length ?? 1, Math.trunc(Number(requested('line'))) || 1)))
const mode = computed(() => file.value ? 'source' : ['contract', 'sources'].includes(requested('view')) ? requested('view') : 'fixture')
const lines = computed(() => {
  if (!file.value) return []
  const start = Math.max(0, line.value - 13)
  const end = Math.min(file.value.length, line.value + 27)
  return file.value.slice(start, end).map((text, i) => ({ text, number: start + i + 1 }))
})
const views = [
  { id: 'fixture', label: 'Fixture' },
  { id: 'contract', label: 'Contract' },
  { id: 'sources', label: 'Sources' },
]

function href(params) { return '?' + new URLSearchParams(params) }
function entryHref(id, view) { return href({ entry: id, ...(view && view !== 'fixture' ? { view } : {}) }) }
function sourceHref(ref) {
  if (!ref.href?.startsWith('?')) return ref.href
  const params = new URLSearchParams(ref.href)
  params.set('entry', entry.value.id)
  return href(params)
}
function open(href, focus = true) {
  history.pushState(null, '', href)
  locationState.value = new URLSearchParams(location.search)
  mobileIndex.value = false
  scrollToTarget(focus)
}
function scrollToTarget(focus) {
  nextTick(() => {
    const section = entry.value.sections.find(item => item.anchor === requested('section') || String(item.line) === requested('section'))
    const target = mode.value === 'contract' && section ? document.getElementById('rule-' + section.line) : title.value
    if (focus) {
      target?.setAttribute('tabindex', '-1')
      target?.focus({ preventScroll: true })
    }
    if (target === title.value) window.scrollTo(0, 0)
    else target?.scrollIntoView({ block: 'start' })
  })
}
function routeLink(event) {
  const a = event.target.closest('a[href^="?"]')
  if (!a || event.defaultPrevented || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || a.target) return
  const params = new URLSearchParams(a.getAttribute('href'))
  if (!params.has('entry')) params.set('entry', entry.value.id)
  event.preventDefault()
  if (standaloneFrame && window.parent !== window) window.parent.location.assign(href(params))
  else open(href(params))
}
function onHistory() { locationState.value = new URLSearchParams(location.search); scrollToTarget(false) }
onMounted(() => { window.addEventListener('popstate', onHistory); if (requested('section')) scrollToTarget(false) })
onUnmounted(() => window.removeEventListener('popstate', onHistory))
watch(() => entry.value.id, id => { document.title = (entries.find(doc => doc.id === id)?.title ?? 'Violet Circuit') + ' · Railgrid design review' })
</script>

<template>
  <main v-if="standaloneFrame" class="review-canvas review-viewport" @click="routeLink"><ReviewStage :stage="entry.review.stage" :catalog="catalog" :entry="entry" /></main>
  <div v-else class="review" @click="routeLink">
    <a class="review-skip k-btn k-btn--primary" href="#review-main">Skip to fixture</a>
    <header class="review-top">
      <a :href="entryHref('design.foundations.system')" class="review-brand"><Hexagon :size="20" :stroke-width="1.5" aria-hidden="true" /><span>Violet Circuit<small>Railgrid design review</small></span></a>
      <div class="review-tools">
        <a class="review-compare" href="http://127.0.0.1:5195/">Impeccable scan</a>
        <label class="review-control">Theme
          <select :value="theme.mode" class="k-input" @change="theme.setMode($event.target.value)">
            <option value="light">Light</option><option value="dark">Dark</option><option value="system">System</option>
          </select>
        </label>
        <label class="review-control" v-if="mode === 'fixture'">Frame
          <select v-model="viewSize" class="k-input">
            <option value="full">Available</option><option value="tablet">760 px</option><option value="phone">390 px</option>
          </select>
        </label>
      </div>
    </header>
    <div class="review-layout">
      <aside class="review-index" :class="{ 'review-index--expanded': mobileIndex }">
        <button type="button" class="k-btn k-btn--ghost review-index-toggle" :aria-expanded="mobileIndex" aria-controls="review-index-body" @click="mobileIndex = !mobileIndex"><BookOpen :size="16" aria-hidden="true" /> Browse the book <span class="review-mono">{{ entries.length }}</span></button>
        <div id="review-index-body" class="review-index-body">
          <label class="review-search"><Search :size="16" aria-hidden="true" /><input v-model="query" class="k-input" type="search" placeholder="Find a rule or component" aria-label="Find a rule or component"></label>
          <nav aria-label="Design book entries" class="review-book-nav">
            <div v-for="item in matches" :key="item.key" class="review-index-group">
              <h2>{{ item.label }} <span>{{ item.documents.length }}</span></h2>
              <a v-for="doc in item.documents" :key="doc.id" :href="entryHref(doc.id)" :aria-current="entry.id === doc.id ? 'page' : undefined">{{ doc.title }}</a>
            </div>
            <p v-if="!matches.length" role="status">No entries match. Try a component name or file.</p>
          </nav>
        </div>
      </aside>

      <main id="review-main" tabindex="-1" class="review-main">
        <div class="review-where"><span>{{ group }}</span><ChevronRight :size="13" aria-hidden="true" /><span>{{ entryIndex + 1 }} / {{ entries.length }}</span></div>
        <h1 ref="title" class="review-title">{{ entry.title }}</h1>
        <div class="review-meta">
          <span class="k-badge k-badge--muted">{{ entry.authority.design }}</span>
          <span class="k-badge k-badge--muted">Implementation: {{ entry.implementation.state }}</span>
          <span class="k-badge k-badge--muted">Book evidence: {{ entry.verification.state }}</span>
        </div>
        <nav class="k-tabs review-tabs" aria-label="Review view">
          <a v-for="view in views" :key="view.id" class="k-tab" :class="{ 'k-tab--active': mode === view.id || (mode === 'source' && view.id === 'sources') }" :aria-current="mode === view.id || (mode === 'source' && view.id === 'sources') ? 'page' : undefined" :href="entryHref(entry.id, view.id)">{{ view.label }}</a>
        </nav>

        <template v-if="mode === 'fixture'">
          <div class="review-intro">
            <div><span class="review-kind">{{ entry.review.kind }}</span><h2>{{ entry.review.title }}</h2><p>{{ entry.review.tryThis }}</p></div>
            <a class="review-source-link" :href="sourceHref(entry.sources.find(item => item.role === 'design'))"><BookOpen :size="15" aria-hidden="true" /> Design rule</a>
          </div>
          <div class="review-canvas" :data-size="viewSize">
            <ReviewStage v-if="viewSize === 'full'" :key="entry.id" :stage="entry.review.stage" :catalog="catalog" :entry="entry" />
            <iframe v-else class="review-frame" :title="'Fixture at ' + (viewSize === 'phone' ? '390' : '760') + ' pixels, or available width'" :src="href({ frame: '1', entry: entry.id, theme: theme.resolved })"></iframe>
          </div>
          <section v-if="entry.review.sources.length" class="review-nearby">
            <h2>Source for this fixture</h2>
            <a v-for="item in entry.review.sources" :key="item.path + item.line" :href="sourceHref(item)" class="review-source-link"><Code2 :size="15" aria-hidden="true" /><span>{{ item.path }}:{{ item.line }}</span></a>
          </section>
        </template>

        <div v-else-if="mode === 'contract'" class="review-contract">
          <p v-if="entry.implementation.notes" class="review-boundary">{{ entry.implementation.notes }}</p>
          <details v-if="entry.sections.length" class="review-outline">
            <summary>Jump to a section <span class="review-mono">{{ entry.sections.length }}</span></summary>
            <ol>
              <li v-for="section in entry.sections" :key="section.line" :data-level="section.level"><a :href="href({ entry: entry.id, view: 'contract', section: section.line })">{{ section.title }}</a><a :href="href({ entry: entry.id, source: entry.path, line: section.line })" class="review-mono" :aria-label="'Read source for ' + section.title">L{{ section.line }}</a></li>
            </ol>
          </details>
          <!-- Rendered at build time from repository Markdown with raw HTML disabled. -->
          <article class="review-prose" v-html="entry.html"></article>
        </div>

        <div v-else-if="mode === 'source'" class="review-code-panel">
          <a class="review-source-link" :href="entryHref(entry.id, 'sources')"><ArrowLeft :size="15" aria-hidden="true" /> All sources for {{ entry.title }}</a>
          <h2 class="review-code-title">{{ path }}:{{ line }}</h2>
          <div class="review-code-nav">
            <a v-if="lines[0].number > 1" :href="href({ entry: entry.id, source: path, line: Math.max(1, lines[0].number - 26) })">Earlier lines</a>
            <a v-if="lines.at(-1).number < file.length" :href="href({ entry: entry.id, source: path, line: Math.min(file.length, lines.at(-1).number + 13) })">Later lines</a>
          </div>
          <div class="review-code" tabindex="0" role="region" aria-label="Source excerpt">
            <div v-for="row in lines" :key="row.number" :class="{ 'review-code-current': row.number === line }"><a :href="href({ entry: entry.id, source: path, line: row.number })" :aria-label="'Line ' + row.number">{{ row.number }}</a><code>{{ row.text || ' ' }}</code></div>
          </div>
        </div>

        <section v-else class="review-nearby">
          <h2>Canonical references</h2>
          <ul class="review-reference-list">
            <li v-for="ref in entry.sources" :key="ref.path + ref.line">
              <span class="review-kind">{{ ref.role }}</span>
              <a v-if="ref.href" class="review-source-link" :href="sourceHref(ref)">{{ ref.path }}{{ ref.line ? ':' + ref.line : '' }}</a><code v-else>{{ ref.path }}</code>
            </li>
          </ul>
          <h2>Evidence recorded in the book</h2>
          <ul class="review-reference-list"><li v-for="check in entry.verification.checks" :key="check.kind + check.ref"><span class="review-kind">{{ check.kind }} · {{ check.status }}</span><div>{{ check.ref }}<p v-if="check.evidence">{{ check.evidence }}</p></div></li></ul>
          <template v-if="entry.relatedDocuments.length">
            <h2>Related entries</h2>
            <ul class="review-reference-list"><li v-for="related in entry.relatedDocuments" :key="related.id"><span class="review-kind">{{ related.relation }}</span><a :href="entryHref(related.id)">{{ entries.find(doc => doc.id === related.id)?.title }}</a></li></ul>
          </template>
        </section>

        <footer class="review-pager">
          <a v-if="entries[entryIndex - 1]" :href="entryHref(entries[entryIndex - 1].id)"><ArrowLeft :size="15" aria-hidden="true" /> {{ entries[entryIndex - 1].title }}</a>
          <a v-if="entries[entryIndex + 1]" :href="entryHref(entries[entryIndex + 1].id)">{{ entries[entryIndex + 1].title }} <ArrowRight :size="15" aria-hidden="true" /></a>
        </footer>
      </main>
    </div>
  </div>
</template>
