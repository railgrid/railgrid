import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { deflateSync, inflateSync } from 'node:zlib'

/*
 * Cross-provider Models create-route evidence runner.
 *
 * The page served at MODEL_FORM_FIXTURE_URL must mount the real App Studio and
 * Agents custom elements and provide the host context/fetch contract. This
 * runner deliberately does not normalize screenshot values. The default
 * pair is compared in the route's actual first-run state; the custom pair
 * changes the real provider control through Playwright before capturing.
 * Unmasked image differences are observational: provider capabilities and
 * credential/test guidance differ. The gate checks the design book's shared
 * geometry, accessibility, empty-draft and credential-destination contracts.
 *
 * Example:
 *   MODEL_FORM_FONT_NODE_MODULES="$PWD/portal/node_modules" \
 *   providers/app-studio/portal/node_modules/.bin/vite \
 *     --config hack/models-form-visual/vite.config.mjs --host 127.0.0.1
 *   MODEL_FORM_FIXTURE_URL=http://127.0.0.1:5198 \
 *   MODEL_FORM_OUTPUT="$PWD/models-form-output" \
 *   PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs \
 *   node hack/models-form-visual-regression.mjs
 */

const origin = process.env.MODEL_FORM_FIXTURE_URL || 'http://127.0.0.1:5198'
const outputDir = resolve(process.env.MODEL_FORM_OUTPUT || resolve(tmpdir(), 'railgrid-model-form-visual'))
const playwrightCandidates = [
  process.env.PLAYWRIGHT_MODULE,
  'playwright',
].filter(Boolean)

async function loadPlaywright() {
  let lastError
  for (const candidate of playwrightCandidates) {
    try {
      return await import(candidate)
    } catch (error) {
      lastError = error
    }
  }
  throw new Error(`Unable to load Playwright (${playwrightCandidates.join(', ')}): ${lastError?.message || 'module not found'}`)
}

function routeFor(provider) {
  return provider === 'app' ? 'create/model' : '#/create/model'
}

function routeQuery(provider, theme) {
  return `/?provider=${provider}&theme=${theme}&route=${encodeURIComponent(routeFor(provider))}`
}

function describePairKey(theme, width) {
  return `${theme}-${width}`
}

function scenarioSuffix(scenario) {
  return scenario === 'openai' ? '' : `-${scenario}`
}

async function waitForRealFonts(page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all([
      document.fonts.load('400 14px "Instrument Sans Variable"'),
      document.fonts.load('600 18px "Instrument Sans Variable"'),
      document.fonts.load('400 18px "Archivo Variable"'),
      document.fonts.load('400 12px "IBM Plex Mono"'),
      document.fonts.load('500 12px "IBM Plex Mono"'),
    ])
    await document.fonts.ready
  })
}

async function collect(page, provider, theme, width, scenario) {
  const consoleErrors = []
  const pageErrors = []
  const onConsole = message => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  }
  page.on('console', onConsole)
  page.on('pageerror', error => pageErrors.push(error.message))
  await page.goto(`${origin}${routeQuery(provider, theme)}`, { waitUntil: 'domcontentloaded' })
  await page.locator('.k-create-surface').waitFor({ state: 'visible', timeout: 15000 })
  await waitForRealFonts(page)
  if (scenario === 'custom') {
    const providerControl = page.locator('select[id$="-model-provider"], #model-provider, #agents-model-provider').first()
    await providerControl.waitFor({ state: 'visible', timeout: 15000 })
    await providerControl.selectOption('custom')
  }
  await page.waitForTimeout(50)

  const result = await page.evaluate(({ provider, theme, width, scenario }) => {
    const root = document.querySelector(provider === 'app' ? 'railgrid-provider-app-studio' : 'railgrid-provider-agents')
    const form = root?.querySelector('.k-model-form')
    const rect = node => {
      if (!node) return null
      const box = node.getBoundingClientRect()
      return {
        x: box.x,
        y: box.y,
        width: box.width,
        height: box.height,
        right: box.right,
        bottom: box.bottom,
      }
    }
    const style = node => {
      if (!node) return null
      const computed = getComputedStyle(node)
      return {
        display: computed.display,
        position: computed.position,
        boxSizing: computed.boxSizing,
        fontFamily: computed.fontFamily,
        fontSize: computed.fontSize,
        fontWeight: computed.fontWeight,
        fontStretch: computed.fontStretch,
        lineHeight: computed.lineHeight,
        letterSpacing: computed.letterSpacing,
        color: computed.color,
        backgroundColor: computed.backgroundColor,
        borderRadius: computed.borderRadius,
        borderWidth: [computed.borderTopWidth, computed.borderRightWidth, computed.borderBottomWidth, computed.borderLeftWidth],
        padding: [computed.paddingTop, computed.paddingRight, computed.paddingBottom, computed.paddingLeft],
        margin: [computed.marginTop, computed.marginRight, computed.marginBottom, computed.marginLeft],
        gap: computed.gap,
        rowGap: computed.rowGap,
        columnGap: computed.columnGap,
        minHeight: computed.minHeight,
        minWidth: computed.minWidth,
        width: computed.width,
        height: computed.height,
      }
    }
    const describe = (node, selector = null) => ({
      selector,
      tag: node?.tagName?.toLowerCase() || null,
      id: node?.id || null,
      class: node?.getAttribute?.('class') || null,
      ariaLabel: node?.getAttribute?.('aria-label') || null,
      ariaLabelledby: node?.getAttribute?.('aria-labelledby') || null,
      text: (node?.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 300),
      visibleText: node instanceof HTMLSelectElement
        ? [...node.selectedOptions].map(option => option.textContent || '').join(' ').trim().replace(/\s+/g, ' ')
        : (node?.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 300),
      optionLabels: node instanceof HTMLSelectElement
        ? [...node.options].map(option => ({ value: option.value, label: (option.textContent || '').trim(), disabled: option.disabled }))
        : null,
      value: node && 'value' in node ? node.value : null,
      placeholder: node?.getAttribute?.('placeholder') || null,
      type: node?.getAttribute?.('type') || null,
      disabled: node && 'disabled' in node ? node.disabled : null,
      ariaRequired: node?.getAttribute?.('aria-required') || null,
      tabIndex: node instanceof HTMLElement ? node.tabIndex : null,
      rect: rect(node),
      style: style(node),
    })
    const query = (selector, scope = root) => scope?.querySelector(selector) || null
    const many = (selector, scope = root) => [...(scope?.querySelectorAll(selector) || [])]
    const firstByText = (selector, text, scope = root) => many(selector, scope).find(node => (node.textContent || '').includes(text)) || null
    const fieldFor = node => node?.closest?.('.k-model-form-field') || node?.closest?.('.k-model-form-section') || null
    const sectionFor = node => node?.closest?.('.k-model-form-section') || null

    const nameInput = query('#model-display-name, input[name="name"]', form)
    const providerControl = query('#model-provider, #agents-model-provider, select[id$="-model-provider"], [data-form-select-trigger]', form)
    const connectionSection = sectionFor(providerControl)
    const endpointField = many('.k-model-form-field', connectionSection).find(node => {
      const control = query('#model-base-url, input[name="baseURL"], .k-input', node)
      return Boolean(control) && node !== fieldFor(providerControl)
    }) || null
    const endpointControl = query('#model-base-url, input[name="baseURL"], .k-input', endpointField)
    const credentialInput = query('#model-credential, #agents-model-key, input[name="apiKey"], textarea[name="apiKey"]', form)
    const findModels = firstByText('button', 'Find models', form)
    const modelControl = query('#model-id', form)
    const footer = query('.k-create-actions', form)
    const notices = many('.k-inline-notification, .k-model-form-test-hint, [role="alert"], [role="status"]', form)
    const fields = many('.k-model-form-field', form).map((node, index) => describe(node, `.k-model-form-field[${index}]`))
    const sections = many('.k-model-form-section', form).map((node, index) => ({
      ...describe(node, `.k-model-form-section[${index}]`),
      heading: describe(query('.k-model-form-heading', node)),
      row: describe(query('.k-model-form-row', node)),
      controls: many('input,select,textarea,button,.k-input', node).map(child => describe(child)),
    }))
    const controls = many('input,select,textarea,button', form).map((node, index) => describe(node, `form-control[${index}]`))
    const activeElement = document.activeElement
    const activeWithinRoot = Boolean(activeElement && root?.contains(activeElement))
    const activeComputed = activeWithinRoot ? getComputedStyle(activeElement) : null
    const accentProbe = document.createElement('span')
    accentProbe.style.color = 'var(--color-accent)'
    root.appendChild(accentProbe)
    const accentColor = getComputedStyle(accentProbe).color
    accentProbe.remove()
    const loadedFonts = [...document.fonts].map(font => ({ family: font.family, style: font.style, weight: font.weight, status: font.status }))
    const requiredFontFamilies = ['Instrument Sans Variable', 'Archivo Variable', 'IBM Plex Mono']
    const fontFaces = requiredFontFamilies.map(family => {
      const matches = loadedFonts.filter(font => font.family.replace(/["']/g, '') === family)
      return {
        family,
        faceCount: matches.length,
        loadedFaceCount: matches.filter(font => font.status === 'loaded').length,
        faces: matches,
      }
    })
    return {
      provider,
      theme,
      width,
      scenario,
      viewport: { width: innerWidth, height: innerHeight, devicePixelRatio },
      fontEvidence: {
        documentStatus: document.fonts.status,
        instrumentSans: document.fonts.check('14px "Instrument Sans Variable"'),
        archivo: document.fonts.check('18px "Archivo Variable"'),
        ibmPlexMono: document.fonts.check('12px "IBM Plex Mono"'),
        requiredFamilies: fontFaces,
        allRequiredFacesLoaded: fontFaces.every(font => font.faceCount > 0 && font.loadedFaceCount > 0),
        loadedFonts,
      },
      page: describe(query('.k-create-page')),
      back: describe(query('.k-back-action')),
      backIcon: { ...describe(query('.k-back-action svg')), flexShrink: query('.k-back-action svg') ? getComputedStyle(query('.k-back-action svg')).flexShrink : null },
      header: describe(query('.k-create-header')),
      title: describe(query('.k-create-title')),
      description: describe(query('.k-create-description')),
      form: describe(form),
      body: describe(query('.k-create-body', form)),
      landmarks: {
        nameField: describe(fieldFor(nameInput)),
        nameInput: describe(nameInput),
        providerField: describe(fieldFor(providerControl)),
        providerControl: describe(providerControl),
        endpointField: describe(endpointField),
        endpointControl: describe(endpointControl),
        credentialSection: describe(sectionFor(credentialInput)),
        credentialInput: describe(credentialInput),
        findModels: describe(findModels),
        modelSection: describe(sectionFor(modelControl)),
        modelControl: describe(modelControl),
        footer: describe(footer),
        footerButtons: many('button', footer).map((node, index) => describe(node, `footer-button[${index}]`)),
        blankNotices: notices.map((node, index) => describe(node, `blank-notice[${index}]`)),
      },
      fields,
      modelInputs: many('.k-model-form .k-input', form).map(node => describe(node)),
      sections,
      hints: many('.k-model-form-hint', form).map((node, index) => describe(node, `.k-model-form-hint[${index}]`)),
      testHint: describe(query('.k-model-form-test-hint', form)),
      actions: describe(footer),
      actionButtons: many('.k-create-actions button', form).map((node, index) => describe(node, `action[${index}]`)),
      controls,
      structure: {
        pages: many('.k-create-page').length,
        headers: many('.k-create-header').length,
        titles: many('.k-create-title').length,
        forms: many('.k-model-form').length,
        sections: many('.k-model-form-heading', form).map(node => node.textContent.trim()),
      },
      accessibility: many('input:not([type="hidden"]),select,textarea', form).map(node => ({
        id: node.id,
        labelled: Boolean(node.labels?.length || node.getAttribute('aria-label') || node.getAttribute('aria-labelledby')?.split(/\s+/).every(id => document.getElementById(id))),
        missingDescriptions: (node.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && !document.getElementById(id)),
      })),
      accentColor,
      focus: {
        activeWithinRoot,
        active: activeWithinRoot ? describe(activeElement) : null,
        focusVisible: activeWithinRoot && activeElement instanceof HTMLElement && activeElement.matches(':focus-visible'),
        outline: activeComputed?.outline || null,
        outlineColor: activeComputed?.outlineColor || null,
        outlineOffset: activeComputed?.outlineOffset || null,
        outlineStyle: activeComputed?.outlineStyle || null,
        outlineWidth: activeComputed?.outlineWidth || null,
      },
      rootText: root?.textContent?.trim().replace(/\s+/g, ' ').slice(0, 2400),
      scroll: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
    }
  }, { provider, theme, width, scenario })
  const screenshot = `${outputDir}/${provider}-${theme}-${width}${scenarioSuffix(scenario)}.png`
  await page.locator('.k-create-page').screenshot({ path: screenshot, animations: 'disabled' })
  // Exercise destination changes only after the untouched screenshot. No
  // discovery/test/save is invoked, and this synthetic value never leaves the
  // in-memory draft. Provider API behavior is covered by test-model-connections.
  const providerControl = page.locator('#model-provider')
  const credential = page.locator('#model-credential')
  await credential.fill('fixture-draft-only')
  await providerControl.selectOption(scenario === 'custom' ? 'openai' : 'custom')
  const providerChangeClearedCredential = await credential.inputValue() === ''
  await providerControl.selectOption('custom')
  const endpoint = page.locator('#model-base-url')
  await endpoint.fill('https://fixture.invalid/first')
  await credential.fill('fixture-draft-only')
  await endpoint.fill('https://fixture.invalid/second')
  const endpointChangeClearedCredential = await credential.inputValue() === ''
  const draftCalls = await page.evaluate(() => globalThis.__railgridModelFixture.calls)
  page.off('console', onConsole)
  return {
    ...result, screenshot, consoleErrors, pageErrors,
    behavior: {
      providerChangeClearedCredential,
      endpointChangeClearedCredential,
      writeCalls: draftCalls.filter(call => !['GET', 'HEAD'].includes(call.method)),
    },
  }
}

function checkContracts(result) {
  const checks = []
  const check = (name, passed, evidence) => checks.push({ name, passed: Boolean(passed), evidence })
  const { width, scenario, landmarks: l } = result
  const provider = l.providerControl.rect
  const endpoint = l.endpointControl.rect
  check('one route heading and shared form', result.structure.pages === 1 && result.structure.headers === 1 && result.structure.titles === 1 && result.structure.forms === 1, result.structure)
  check('fluid wide creation surface', result.page.style.display === 'block' && result.page.style.minWidth === '0px' && result.form.rect.width === result.page.rect.width, { page: result.page.rect, form: result.form.rect })
  check('explicit route typography', result.page.style.fontSize === '14px' && result.page.style.lineHeight === '21px', result.page.style)
  check('display heading', result.title.style.fontFamily.includes('Archivo Variable') && result.title.style.fontStretch === '125%' && result.title.style.fontSize === '18px', result.title.style)
  check('back then heading then form', result.back.rect.bottom <= result.header.rect.y && result.header.rect.bottom <= result.form.rect.y, { back: result.back.rect, header: result.header.rect, form: result.form.rect })
  check('fixed back icon', result.backIcon.rect?.width === 14 && result.backIcon.rect?.height === 14 && result.backIcon.flexShrink === '0', result.backIcon)
  check('route heading focus', result.focus.active?.class?.includes('k-create-title') && result.focus.active.tabIndex === -1 && result.focus.outlineStyle === 'solid' && result.focus.outlineWidth === '2px' && result.focus.outlineOffset === '2px' && result.focus.outlineColor === result.accentColor, result.focus)
  check('named controls with resolvable help', result.accessibility.length > 0 && result.accessibility.every(control => control.labelled && control.missingDescriptions.length === 0), result.accessibility)
  check('shared section hierarchy', result.structure.sections.join('|') === 'Connection|Credential|Model', result.structure.sections)
  check('provider and endpoint equal control height', provider && endpoint && provider.height === endpoint.height, { provider, endpoint })
  check(width < 640 ? 'connection controls stack on mobile' : 'connection controls top-align on desktop', provider && endpoint && (width < 640 ? endpoint.y > provider.y : endpoint.y === provider.y), { provider, endpoint })
  check('model field sizes', result.modelInputs.length > 0 && result.modelInputs.every(control => control.rect && (width < 640 ? control.rect.height >= 44 : control.rect.height === 40)), result.modelInputs.map(control => ({ id: control.id, height: control.rect?.height })))
  check('no horizontal overflow', result.scroll.width <= result.viewport.width, { scroll: result.scroll, viewport: result.viewport })
  check('real host fonts loaded', result.fontEvidence.allRequiredFacesLoaded, result.fontEvidence.requiredFamilies)
  check('no browser errors', result.consoleErrors.length === 0 && result.pageErrors.length === 0, { consoleErrors: result.consoleErrors, pageErrors: result.pageErrors })
  check('empty new draft and explicit provider', l.nameInput.value === '' && l.credentialInput.value === '' && l.modelControl.value === '' && l.providerControl.value === (scenario === 'custom' ? 'custom' : 'openai'), { name: l.nameInput.value, credential: l.credentialInput.value, model: l.modelControl.value, provider: l.providerControl.value })
  check('credential stays a password field', l.credentialInput.type === 'password', l.credentialInput.type)
  check('explicit custom endpoint', scenario === 'custom' ? l.endpointControl.tag === 'input' && l.endpointControl.type === 'url' && l.endpointControl.ariaRequired === 'true' : l.endpointControl.tag === 'div' && l.endpointControl.text === 'https://api.openai.com/v1', l.endpointControl)
  check('Cancel then test then Connect with verification gate', result.actionButtons.map(button => button.text).join('|') === 'Cancel|Test connection|Connect model' && result.actionButtons[1].disabled && result.actionButtons[2].disabled && l.findModels.disabled, result.actionButtons.map(button => ({ text: button.text, disabled: button.disabled })))
  if (width < 640) check('coarse pointer action targets', result.back.rect.height >= 44 && result.back.rect.width >= 44 && result.actionButtons.every(button => button.rect.height >= 44), { back: result.back.rect, actions: result.actionButtons.map(button => button.rect) })
  check('provider change clears typed credential', result.behavior.providerChangeClearedCredential, result.behavior)
  check('endpoint change clears typed credential', result.behavior.endpointChangeClearedCredential, result.behavior)
  check('draft provider changes do not mutate saved resources', result.behavior.writeCalls.length === 0, result.behavior.writeCalls)
  return checks
}

function checkSharedGeometry(app, agents) {
  // These landmarks precede provider-owned help, extra controls and notices.
  // Later section/footer positions legitimately follow their content height.
  const landmarks = ['back', 'header', 'title', 'description']
  const checks = landmarks.map(name => ({
    name: `shared ${name} geometry`,
    passed: ['x', 'y', 'width', 'height'].every(key => app[name].rect[key] === agents[name].rect[key]),
    evidence: { app: app[name].rect, agents: agents[name].rect },
  }))
  for (const name of ['nameInput', 'providerControl', 'endpointControl']) {
    // The stacked endpoint follows provider-owned help. Its y coordinate may
    // differ on mobile when that help wraps to a different number of lines.
    const dimensions = name === 'endpointControl' && app.width < 640 ? ['x', 'width', 'height'] : ['x', 'y', 'width', 'height']
    checks.push({
      name: `shared ${name} geometry`,
      passed: dimensions.every(key => app.landmarks[name].rect[key] === agents.landmarks[name].rect[key]),
      evidence: { app: app.landmarks[name].rect, agents: agents.landmarks[name].rect },
    })
  }
  return checks
}

// Minimal PNG RGBA decoder/encoder keeps the regression self-contained. The
// browser screenshots are 8-bit PNGs; accepting RGB/gray inputs makes the
// comparison useful with checked-in baselines from other Chromium versions.
function paeth(a, b, c) {
  const p = a + b - c
  const pa = Math.abs(p - a)
  const pb = Math.abs(p - b)
  const pc = Math.abs(p - c)
  return pa <= pb && pa <= pc ? a : pb <= pc ? b : c
}

function decodePng(buffer) {
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])
  assert.deepEqual(buffer.subarray(0, 8), signature, 'visual artifact is not a PNG')
  let offset = 8
  let width
  let height
  let bitDepth
  let colorType
  let interlace
  const idat = []
  while (offset < buffer.length) {
    const length = buffer.readUInt32BE(offset)
    const type = buffer.toString('ascii', offset + 4, offset + 8)
    const data = buffer.subarray(offset + 8, offset + 8 + length)
    offset += length + 12
    if (type === 'IHDR') {
      width = data.readUInt32BE(0)
      height = data.readUInt32BE(4)
      bitDepth = data[8]
      colorType = data[9]
      interlace = data[12]
    } else if (type === 'IDAT') {
      idat.push(data)
    } else if (type === 'IEND') {
      break
    }
  }
  assert.equal(bitDepth, 8, `unsupported PNG bit depth ${bitDepth}`)
  assert.equal(interlace, 0, 'interlaced PNGs are unsupported')
  const channels = { 0: 1, 2: 3, 4: 2, 6: 4 }[colorType]
  assert.ok(channels, `unsupported PNG color type ${colorType}`)
  const stride = width * channels
  const decoded = inflateSync(Buffer.concat(idat))
  const pixels = Buffer.alloc(width * height * 4)
  const prior = Buffer.alloc(stride)
  let sourceOffset = 0
  for (let y = 0; y < height; y += 1) {
    const filter = decoded[sourceOffset++]
    const row = Buffer.alloc(stride)
    for (let x = 0; x < stride; x += 1) {
      const raw = decoded[sourceOffset++]
      const left = x >= channels ? row[x - channels] : 0
      const up = prior[x] || 0
      const upperLeft = x >= channels ? prior[x - channels] || 0 : 0
      row[x] = (raw + (filter === 0 ? 0 : filter === 1 ? left : filter === 2 ? up : filter === 3 ? Math.floor((left + up) / 2) : paeth(left, up, upperLeft))) & 0xff
    }
    for (let x = 0; x < width; x += 1) {
      const source = x * channels
      const target = (y * width + x) * 4
      if (colorType === 6) pixels.set(row.subarray(source, source + 4), target)
      else if (colorType === 2) {
        pixels[target] = row[source]
        pixels[target + 1] = row[source + 1]
        pixels[target + 2] = row[source + 2]
        pixels[target + 3] = 255
      } else if (colorType === 4) {
        pixels[target] = row[source]
        pixels[target + 1] = row[source]
        pixels[target + 2] = row[source]
        pixels[target + 3] = row[source + 1]
      } else {
        pixels[target] = row[source]
        pixels[target + 1] = row[source]
        pixels[target + 2] = row[source]
        pixels[target + 3] = 255
      }
    }
    row.copy(prior)
  }
  return { width, height, pixels }
}

function crc32(buffer) {
  let crc = 0xffffffff
  for (const byte of buffer) {
    crc ^= byte
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
  }
  return (crc ^ 0xffffffff) >>> 0
}

function pngChunk(type, data) {
  const name = Buffer.from(type, 'ascii')
  const body = Buffer.concat([name, data])
  const chunk = Buffer.alloc(12 + data.length)
  chunk.writeUInt32BE(data.length, 0)
  body.copy(chunk, 4)
  chunk.writeUInt32BE(crc32(body), 8 + data.length)
  return chunk
}

function encodePng(image) {
  const rows = Buffer.alloc((image.width * 4 + 1) * image.height)
  for (let y = 0; y < image.height; y += 1) {
    const start = y * (image.width * 4 + 1)
    rows[start] = 0
    image.pixels.copy(rows, start + 1, y * image.width * 4, (y + 1) * image.width * 4)
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(image.width, 0)
  header.writeUInt32BE(image.height, 4)
  header[8] = 8
  header[9] = 6
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    pngChunk('IHDR', header),
    pngChunk('IDAT', deflateSync(rows)),
    pngChunk('IEND', Buffer.alloc(0)),
  ])
}

function comparePngs(left, right) {
  const width = Math.max(left.width, right.width)
  const height = Math.max(left.height, right.height)
  const diff = Buffer.alloc(width * height * 4)
  let differingPixels = 0
  let totalAbsoluteDifference = 0
  let maxChannelDifference = 0
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const target = (y * width + x) * 4
      const leftTarget = (y * left.width + x) * 4
      const rightTarget = (y * right.width + x) * 4
      const leftInside = x < left.width && y < left.height
      const rightInside = x < right.width && y < right.height
      const channels = [0, 1, 2, 3]
      let changed = !leftInside || !rightInside
      let delta = 0
      for (const channel of channels) {
        const a = leftInside ? left.pixels[leftTarget + channel] : 0
        const b = rightInside ? right.pixels[rightTarget + channel] : 0
        const channelDelta = Math.abs(a - b)
        delta += channelDelta
        maxChannelDifference = Math.max(maxChannelDifference, channelDelta)
        if (channelDelta !== 0) changed = true
      }
      totalAbsoluteDifference += delta
      if (changed) {
        differingPixels += 1
        diff[target] = 255
        diff[target + 1] = 40
        diff[target + 2] = 40
        diff[target + 3] = 220
      }
    }
  }
  return {
    width,
    height,
    equal: left.width === right.width && left.height === right.height && differingPixels === 0,
    differingPixels,
    totalPixels: width * height,
    differingRatio: width * height ? differingPixels / (width * height) : 0,
    meanAbsoluteChannelDifference: width * height ? totalAbsoluteDifference / (width * height * 4) : 0,
    maxChannelDifference,
    diff,
  }
}

async function run() {
  await mkdir(outputDir, { recursive: true })
  const { chromium } = await loadPlaywright()
  const browser = await chromium.launch({ headless: true })
  const results = []
  try {
    for (const theme of ['light', 'dark']) {
      for (const width of [1440, 390]) {
        for (const provider of ['app', 'agents']) {
          for (const scenario of ['openai', 'custom']) {
            const context = await browser.newContext({
              viewport: { width, height: 1200 },
              deviceScaleFactor: 1,
              hasTouch: width < 700,
            })
            const page = await context.newPage()
            results.push(await collect(page, provider, theme, width, scenario))
            await context.close()
          }
        }
      }
    }
  } finally {
    await browser.close()
  }

  const byKey = new Map(results.map(result => [`${result.provider}-${describePairKey(result.theme, result.width)}-${result.scenario}`, result]))
  const visualDiffs = []
  const sharedGeometry = []
  for (const result of results) result.contractChecks = checkContracts(result)
  for (const theme of ['light', 'dark']) {
    for (const width of [1440, 390]) {
      for (const scenario of ['openai', 'custom']) {
        const key = `${describePairKey(theme, width)}-${scenario}`
        const app = byKey.get(`app-${key}`)
        const agents = byKey.get(`agents-${key}`)
        assert.ok(app && agents, `missing ${key} captures`)
        sharedGeometry.push({ theme, viewportWidth: width, scenario, checks: checkSharedGeometry(app, agents) })
        const left = decodePng(await readFile(app.screenshot))
        const right = decodePng(await readFile(agents.screenshot))
        const { diff: diffPixels, ...metrics } = comparePngs(left, right)
        const suffix = scenarioSuffix(scenario)
        const diffPath = `${outputDir}/app-vs-agents-${theme}-${width}${suffix}.diff.png`
        await writeFile(diffPath, encodePng({ width: metrics.width, height: metrics.height, pixels: diffPixels }))
        visualDiffs.push({
          theme,
          viewportWidth: width,
          scenario,
          role: 'observational; full unmasked provider differences',
          appScreenshot: app.screenshot,
          agentsScreenshot: agents.screenshot,
          diff: diffPath,
          cropWidth: metrics.width,
          cropHeight: metrics.height,
          equal: metrics.equal,
          differingPixels: metrics.differingPixels,
          totalPixels: metrics.totalPixels,
          differingRatio: metrics.differingRatio,
          meanAbsoluteChannelDifference: metrics.meanAbsoluteChannelDifference,
          maxChannelDifference: metrics.maxChannelDifference,
        })
      }
    }
  }

  const failedContracts = results.flatMap(result => result.contractChecks.filter(check => !check.passed).map(check => ({ provider: result.provider, theme: result.theme, width: result.width, scenario: result.scenario, ...check })))
  const failedGeometry = sharedGeometry.flatMap(pair => pair.checks.filter(check => !check.passed).map(check => ({ theme: pair.theme, width: pair.viewportWidth, scenario: pair.scenario, ...check })))
  const report = { origin, outputDir, results, sharedGeometry, visualDiffs, failedContracts, failedGeometry }
  const reportPath = `${outputDir}/models-form-visual-regression.json`
  await writeFile(reportPath, JSON.stringify(report, null, 2))
  console.log(JSON.stringify({ report: reportPath, captures: results.length, failedContracts, failedGeometry, visualDiffs }, null, 2))
  if (failedContracts.length || failedGeometry.length) process.exitCode = 1
}

await run()
