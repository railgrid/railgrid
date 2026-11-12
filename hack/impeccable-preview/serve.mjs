import { createServer } from 'node:http'
import { readFileSync, existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = fileURLToPath(new URL('.', import.meta.url))
const root = resolve(here, '../..')
const modules = process.env.RAILGRID_PORTAL_MODULES || resolve(root, 'portal/node_modules')
const require = createRequire(resolve(modules, 'vite/package.json'))
const MarkdownIt = require('markdown-it')
const markdown = new MarkdownIt({ html: false })
const read = path => readFileSync(resolve(root, path), 'utf8')
const snapshot = JSON.parse(read('hack/impeccable-preview/snapshot.json'))
const refs = new Set([...snapshot.refs.flat().map(r => r.path), snapshot.hostPath,
  'docs/design/README.md', 'docs/design/patterns/fluid-shell.md', 'portal/src/main.ts',
  'portal/src/stores/theme.ts', 'hack/dex/web/static/main.css'])
const staticFiles = new Map([
  ['/', resolve(here, 'index.html')], ['/preview.css', resolve(here,'preview.css')],
  ['/preview.js',resolve(here,'preview.js')], ['/snapshot.json',resolve(here,'snapshot.json')],
  ['/DESIGN.md',resolve(root,'DESIGN.md')], ['/.impeccable/design.json',resolve(root,'.impeccable/design.json')],
  ['/fonts/instrument.woff2',resolve(modules,'@fontsource-variable/instrument-sans/files/instrument-sans-latin-wght-normal.woff2')],
  ['/fonts/archivo.woff2',resolve(modules,'@fontsource-variable/archivo/files/archivo-latin-wdth-normal.woff2')],
  ['/fonts/mono.woff2',resolve(modules,'@fontsource/ibm-plex-mono/files/ibm-plex-mono-latin-400-normal.woff2')],
  ['/dex',resolve(here,'dex.html')], ['/dex/static/main.css',resolve(root,'hack/dex/web/static/main.css')],
])
const types = {'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.json':'application/json','.md':'text/plain; charset=utf-8','.woff2':'font/woff2','.svg':'image/svg+xml'}
function sourceHash(path, line) { return '#source?' + new URLSearchParams({path,line}) }
const body = read('DESIGN.md').replace(/^---\n[\s\S]*?\n---\n/, '')
const tokens = markdown.parse(body,{})
for(const token of tokens){
  for(const child of token.children ?? []){
    if(child.type !== 'link_open') continue
    const [path,line] = child.attrGet('href').split('#L')
    if(refs.has(path)) child.attrSet('href',sourceHash(path,line || 1))
  }
}
const documentHTML = markdown.renderer.render(tokens, markdown.options, {})
const port = Number(process.env.RAILGRID_IMPECCABLE_PORT || 5195)
createServer((request,response) => {
  const url = new URL(request.url, 'http://127.0.0.1')
  const path = url.pathname
  function send(type, value, status = 200) {
    response.writeHead(status,{ 'content-type':type, 'cache-control':'no-store', 'x-content-type-options':'nosniff' })
    response.end(value)
  }
  if(path === '/theme.css') return send(types['.css'],snapshot.themeCSS)
  if(path === '/document') return send(types['.html'],documentHTML)
  if(path === '/source'){
    const file=url.searchParams.get('path')
    if(!refs.has(file)) return send(types['.json'],JSON.stringify({path:'Source not available',line:1,rows:[]}),404)
    const text=read(file).split('\n')
    const line=Math.max(1,Math.min(text.length,Number(url.searchParams.get('line')) || 1))
    const start=Math.max(0,line-9)
    return send(types['.json'],JSON.stringify({path:file,line,rows:text.slice(start,line+18).map((text,i)=>({line:start+i+1,text}))}))
  }
  let file=staticFiles.get(path)
  if(!file && /^\/dex\/static\/(?:img|fonts)\/[\w.-]+\.(?:svg|woff2)$/.test(path)) file=resolve(root,'hack/dex/web',path.slice('/dex/'.length))
  if(!file || !existsSync(file)) return send('text/plain','Not found',404)
  send(types[extname(file)] || 'application/octet-stream',readFileSync(file))
}).listen(port,'127.0.0.1',() => console.log('Railgrid Impeccable preview: http://127.0.0.1:' + port + '/'))
