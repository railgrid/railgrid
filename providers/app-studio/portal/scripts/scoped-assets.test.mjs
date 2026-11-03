import assert from 'node:assert/strict'
import { test } from 'node:test'
import { Script } from 'node:vm'
import { scopedProviderAssets } from './scoped-assets.mjs'

test('bootstrap captures its own granted directory; lazy chunks retain relative scope', () => {
  const bundle = {
    main: { type: 'chunk', isEntry: true, fileName: 'main.js', code: '(()=>{const load=()=>import("/ui/providers/app-studio/assets/page.js");const css=(function(e){return"/ui/providers/app-studio/"+e})("assets/page.css");globalThis.scopedCSS=css;})();' },
    page: { type: 'chunk', isEntry: false, fileName: 'assets/page.js', code: 'import "./shared.js";export const image="/ui/providers/app-studio/assets/icon.svg";' },
  }
  scopedProviderAssets('/ui/providers/app-studio/').generateBundle.handler({}, bundle)
  const script = new Script(bundle.main.code)
  const context = { URL, document: { currentScript: { src: 'https://console.example/ui/providers/app-studio/_grant/sealed/main.js' } } }
  script.runInNewContext(context)
  assert.equal(context.scopedCSS, 'https://console.example/ui/providers/app-studio/_grant/sealed/assets/page.css')
  assert.match(bundle.main.code, /document.currentScript.src/)
  assert.doesNotMatch(bundle.main.code, /import.meta/)
  assert.ok(bundle.main.code.includes('new URL("assets/page.js",__railgridAssetBase).href'))
  assert.match(bundle.page.code, /new URL\("\.\.\/",import.meta.url\)/)
  assert.match(bundle.page.code, /import "\.\/shared.js"/)
  assert.doesNotMatch(bundle.main.code + bundle.page.code, /"\/ui\/providers\/app-studio\//)
})
