import assert from 'node:assert/strict'
import { test } from 'node:test'
import { Script } from 'node:vm'
import { readFile } from 'node:fs/promises'
import { scopedProviderAssets } from './scoped-assets.mjs'

test('lazy chunks and CSS resolve under the bootstrap grant directory', () => {
  const bundle = {
    main: { type: 'chunk', isEntry: true, fileName: 'main.js', code: '(()=>{const load=()=>import("/ui/providers/app-studio/assets/page.js");const css=(function(e){return"/ui/providers/app-studio/"+e})("assets/page.css");globalThis.scopedCSS=css;globalThis.icon="/ui/providers/app-studio/icon.svg";})();' },
    page: { type: 'chunk', isEntry: false, fileName: 'assets/page.js', code: 'import "./shared.js";export const icon="/ui/providers/app-studio/assets/icon.svg";' },
    styles: { type: 'asset', fileName: 'assets/page.css', source: '@font-face{src:url("/ui/providers/app-studio/assets/font.woff2")} .icon{background:url(/ui/providers/app-studio/assets/icon.svg)}' },
  }
  scopedProviderAssets('/ui/providers/app-studio/').generateBundle.handler({}, bundle)
  const context = { URL, document: { currentScript: { src: 'https://console.example/ui/providers/app-studio/_grant/fpui_sealed/main.js' } } }
  new Script(bundle.main.code).runInNewContext(context)
  assert.equal(context.scopedCSS, 'https://console.example/ui/providers/app-studio/_grant/fpui_sealed/assets/page.css')
  assert.equal(context.icon, 'https://console.example/ui/providers/app-studio/_grant/fpui_sealed/icon.svg')
  assert.match(bundle.main.code, /document.currentScript.src/)
  assert.ok(bundle.main.code.includes('new URL("assets/page.js",__railgridAssetBase).href'))
  assert.match(bundle.page.code, /new URL\("\.\.\/",import.meta.url\)/)
  assert.match(bundle.page.code, /import "\.\/shared.js"/)
  assert.doesNotMatch(bundle.main.code + bundle.page.code, /"\/ui\/providers\/app-studio\//)
  assert.equal(bundle.styles.source, '@font-face{src:url("./font.woff2")} .icon{background:url(./icon.svg)}')
})

test('App Studio installs the scoped asset plugin after classic bootstrap wrapping', async () => {
  const config = await readFile(new URL('../vite.config.ts', import.meta.url), 'utf8')
  assert.match(config, /import \{ scopedProviderAssets \} from '\.\/scripts\/scoped-assets\.mjs'/)
  assert.ok(config.indexOf('isolateClassicBootstrap(),') < config.indexOf("scopedProviderAssets('/ui/providers/app-studio/'),"))
})
