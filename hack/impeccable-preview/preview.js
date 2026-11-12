const $ = selector => document.querySelector(selector)
const make = (tag, value, className) => {
  const el = document.createElement(tag)
  if (value !== undefined) el.textContent = value
  if (className) el.className = className
  return el
}
const system = matchMedia('(prefers-color-scheme: dark)')
const theme = $('#theme')
try { theme.value = localStorage.getItem('impeccable-violet-circuit-theme') || 'light' } catch {}
function applyTheme() {
  const mode = theme.value || 'light'
  const resolved = mode === 'system' ? system.matches ? 'dark' : 'light' : mode
  document.documentElement.classList.toggle('light', resolved === 'light')
  document.documentElement.classList.toggle('dark', resolved === 'dark')
  try { localStorage.setItem('impeccable-violet-circuit-theme', mode) } catch {}
}
theme.addEventListener('change', applyTheme)
system.addEventListener('change', applyTheme)
applyTheme()

const sourceHash = (path,line) => '#source?' + new URLSearchParams({path,line})
async function view() {
  const [hash,params] = location.hash.slice(1).split('?')
  const active = ['design','tokens','source'].includes(hash) ? hash : 'specimens'
  for(const id of ['specimens','design','tokens','source']) $('#' + id).hidden = id !== active
  for(const link of document.querySelectorAll('.views a[href^="#"]')) {
    if(link.hash === '#' + active) link.setAttribute('aria-current','page')
    else link.removeAttribute('aria-current')
  }
  if(active !== 'source') return
  const data = await fetch('/source?' + params).then(response => response.json())
  $('#source-title').textContent = data.path + ':' + data.line
  $('#source-code').replaceChildren(...data.rows.map(row => make('div', String(row.line).padStart(5,' ') + '  ' + row.text, row.line === data.line ? 'current' : '')))
}
window.addEventListener('hashchange', () => { view().catch(showError); window.scrollTo(0,0) })
function showError(error) { $('#error').hidden = false; $('#error').textContent = error.message }
try {
  const [sidecar, snapshot, documentHTML] = await Promise.all([
    fetch('/.impeccable/design.json').then(r => r.json()), fetch('/snapshot.json').then(r => r.json()), fetch('/document').then(r => r.text()),
  ])
  $('#prose').innerHTML = documentHTML // Repository Markdown, rendered server-side with raw HTML disabled.
  sidecar.components.forEach((spec, i) => {
    const item = make('article', undefined, 'sample')
    const info = make('div')
    info.append(make('h2',spec.name),make('p',spec.description))
    const sources = make('div',undefined,'sample-source')
    for(const ref of snapshot.refs[i]){
      const link = make('a',ref.path.split('/').at(-1) + ':' + ref.line)
      link.href = sourceHash(ref.path,ref.line)
      link.title = ref.path + ':' + ref.line
      sources.append(link)
    }
    info.append(sources)
    const controls = make('div',undefined,'sample-controls')
    const host = make('div',undefined,'sample-host')
    const shadow = host.attachShadow({mode:'open'})
    shadow.append(make('style',spec.css))
    const content = make('template')
    content.innerHTML = spec.html // Generated from trusted local source.
    shadow.append(content.content)
    shadow.addEventListener('click',event => {
      const control = event.target.closest('a,button')
      if (!control) return
      if(control.matches('a')){
        event.preventDefault()
        control.closest('nav').querySelector('[aria-current]')?.removeAttribute('aria-current')
        control.setAttribute('aria-current','page')
      }
      $('#interaction').textContent = control.textContent.trim() + ' selected in the fixture.'
    })
    controls.append(host)
    if(spec.kind === 'button' || spec.kind === 'input'){
      const label=make('label')
      const toggle=make('input')
      toggle.type='checkbox'
      toggle.addEventListener('change',() => { shadow.querySelector('button,input').disabled=toggle.checked })
      label.append(toggle, document.createTextNode(' Show disabled state'))
      controls.append(label)
    }
    item.append(info,controls)
    $('#items').append(item)
  })
  for(const [key, value] of Object.entries(snapshot.light)){
    if(!key.startsWith('--color-')) continue
    const tr = make('tr')
    const name=make('th',key);name.scope='row'
    tr.append(name)
    for(const [css,line] of [[value,snapshot.tokenLines[key][1]],[snapshot.base[key],snapshot.tokenLines[key][0]]]){
      const cell=make('td')
      const swatch=make('span',undefined,'swatch')
      swatch.style.background=css; swatch.setAttribute('aria-hidden','true')
      const link=make('a',css);link.href=sourceHash(snapshot.hostPath,line)
      cell.append(swatch,link)
      tr.append(cell)
    }
    $('#token-rows').append(tr)
  }
  await view()
} catch(error){showError(error)}
