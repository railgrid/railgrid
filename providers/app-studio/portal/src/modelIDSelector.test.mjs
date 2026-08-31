import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-app-studio-model-id-selector',
  configFile: false,
  plugins: [vue()],
  server: { middlewareMode: true },
})
const { default: ModelIDSelector } = await vite.ssrLoadModule('/src/ModelIDSelector.vue')
test.after(async () => vite.close())

test('renders a Faros-owned combobox trigger instead of a native datalist', async () => {
  const html = await renderToString(createSSRApp(ModelIDSelector, {
    modelValue: 'gpt-5.4',
    models: [
      { id: 'gpt-5.4', name: 'GPT 5.4', compatibility: 'recommended' },
      { id: 'o4-mini', name: 'o4 mini', compatibility: 'available' },
    ],
    describedBy: 'model-id-hint',
  }))

  assert.match(html, /id="model-id"/)
  assert.match(html, /aria-haspopup="listbox"/)
  assert.match(html, /aria-describedby="model-id-hint"/)
  assert.match(html, /GPT 5\.4/)
  assert.doesNotMatch(html, /<datalist/)
})

test('implements the filter-selector search and full-catalog interaction pattern', async () => {
  const source = await readFile(new URL('./ModelIDSelector.vue', import.meta.url), 'utf8')

  assert.match(source, /query\.value = ''[\s\S]*open\.value = true/)
  assert.match(source, /modelSelectorOptions\(props\.models, query\.value\)/)
  assert.match(source, /k-menu k-table__filter-panel k-table__filter-panel--searchable/)
  assert.match(source, /role="combobox"[\s\S]*aria-autocomplete="list"/)
  assert.match(source, /\{\{ optionSummary \}\}/)
  assert.match(source, /Recommended/)
  assert.match(source, /Not for chat/)
  assert.match(source, /Use “\$\{option\.label\}”/)
})
