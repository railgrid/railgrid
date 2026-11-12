import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

// Model the DOM operations used by the closed control. Popup layout and
// assistive-technology announcements require a browser.
class TestElement extends EventTarget {
  attributes = new Map()
  children = []
  classList = { toggle() {} }
  dataset = {}
  parent = null
  textContent = ''

  get firstElementChild() { return this.children[0] ?? null }
  get lastElementChild() { return this.children.at(-1) ?? null }

  append(child) {
    child.parent = this
    this.children.push(child)
  }

  replaceChildren(child) {
    for (const current of [...this.children]) current.remove()
    this.append(child)
  }

  remove() {
    this.parent.children = this.parent.children.filter(child => child !== this)
    this.parent = null
  }

  setAttribute(name, value) { this.attributes.set(name, value) }
  getAttribute(name) { return this.attributes.get(name) ?? null }
  removeAttribute(name) { this.attributes.delete(name) }
}

const document = Object.assign(new EventTarget(), {
  createElement: () => new TestElement(),
})
const source = readFileSync(new URL('./form-select.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
})
const exports = {}
const dependencies = new Map([
  ['./icons', { ic: () => '' }],
  ['./styles', { ensureRailgridUIStyles() {} }],
])
runInNewContext(outputText, {
  exports,
  require: name => dependencies.get(name),
  HTMLElement: TestElement,
  document,
  window: new EventTarget(),
}, { filename: 'form-select.ts' })

test('FormSelect exposes and clears its validation alert and description', () => {
  const select = new exports.FormSelectElement()
  select.connectedCallback()
  const root = select.firstElementChild
  const trigger = root.firstElementChild
  const findError = () => root.children.find(child => child.className === 'k-form-select__error')

  select.errorMessage = 'Select a region.'
  assert.equal(findError(), undefined)
  assert.equal(trigger.getAttribute('aria-describedby'), null)

  select.invalid = true
  const error = findError()
  assert.ok(error)
  assert.ok(error.id)
  assert.equal(error.getAttribute('role'), 'alert')
  assert.equal(error.lastElementChild.textContent, 'Select a region.')
  assert.equal(trigger.getAttribute('aria-invalid'), 'true')
  assert.equal(trigger.getAttribute('aria-describedby'), error.id)

  select.invalid = false
  assert.equal(findError(), undefined)
  assert.equal(trigger.getAttribute('aria-invalid'), null)
  assert.equal(trigger.getAttribute('aria-describedby'), null)

  select.describedby = 'region-help'
  select.invalid = true
  const nextError = findError()
  assert.ok(nextError)
  assert.equal(trigger.getAttribute('aria-describedby'), `region-help ${nextError.id}`)

  select.errorMessage = 'Select an available region.'
  assert.equal(findError(), nextError)
  assert.equal(nextError.lastElementChild.textContent, 'Select an available region.')

  select.errorMessage = ''
  assert.equal(findError(), undefined)
  assert.equal(trigger.getAttribute('aria-invalid'), 'true')
  assert.equal(trigger.getAttribute('aria-describedby'), 'region-help')
})
