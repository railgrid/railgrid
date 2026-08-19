import assert from 'node:assert/strict'
import test from 'node:test'

import { inlineCssAssets } from '../inline-css-assets.mjs'

test('inlines emitted Vue component CSS into the IIFE entry chunk', () => {
  const bundle = {
    'main.js': {
      type: 'chunk',
      fileName: 'main.js',
      isEntry: true,
      code: 'globalThis.appStudioLoaded = true;',
    },
    'main.css': {
      type: 'asset',
      fileName: 'main.css',
      source: '.pk-overlay{position:fixed;inset:0}',
    },
    'icon.svg': {
      type: 'asset',
      fileName: 'icon.svg',
      source: '<svg/>',
    },
  }

  const plugin = inlineCssAssets({ styleId: 'app-studio-component-css' })
  assert.equal(plugin.enforce, 'post')
  plugin.generateBundle({}, bundle)

  assert.equal(bundle['main.css'], undefined)
  assert.ok(bundle['icon.svg'])
  assert.match(bundle['main.js'].code, /app-studio-component-css/)
  assert.match(bundle['main.js'].code, /\.pk-overlay\{position:fixed;inset:0\}/)
  assert.match(bundle['main.js'].code, /document\.head\.appendChild\(style\)/)
  assert.match(bundle['main.js'].code, /globalThis\.appStudioLoaded = true;/)
})

test('leaves an entry chunk unchanged when Vite emits no CSS asset', () => {
  const bundle = {
    'main.js': {
      type: 'chunk',
      fileName: 'main.js',
      isEntry: true,
      code: 'globalThis.appStudioLoaded = true;',
    },
  }

  const plugin = inlineCssAssets({ styleId: 'app-studio-component-css' })
  plugin.generateBundle({}, bundle)

  assert.equal(bundle['main.js'].code, 'globalThis.appStudioLoaded = true;')
})
