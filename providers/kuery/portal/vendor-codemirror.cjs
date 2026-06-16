// Concatenate the CodeMirror 5 UMD core + the addons we use into a single
// vendored bundle (JS + CSS) under dist/, mirroring how cytoscape is vendored.
// The portal build is IIFE library mode (no runtime module loader), so the
// editor is lazy-injected via <script>/<link> on first playground open and read
// off the window.CodeMirror global the UMD bundle defines.
const fs = require('fs')
const base = 'node_modules/codemirror/'

const js = [
  'lib/codemirror.js',
  'mode/javascript/javascript.js', // JSON is javascript mode with json:true
  'addon/hint/show-hint.js',
  'addon/edit/closebrackets.js',
  'addon/edit/matchbrackets.js',
].map((f) => fs.readFileSync(base + f, 'utf8')).join('\n;\n')
fs.writeFileSync('dist/codemirror.bundle.js', js)

const css = [
  'lib/codemirror.css',
  'addon/hint/show-hint.css',
].map((f) => fs.readFileSync(base + f, 'utf8')).join('\n')
fs.writeFileSync('dist/codemirror.bundle.css', css)

console.log('vendored codemirror.bundle.{js,css}')
