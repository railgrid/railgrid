import { describe, expect, it } from 'vitest'
import {
  ensureRailgridUIStyles,
  RAILGRID_UI_CANONICAL_MARKER,
  RAILGRID_UI_CANONICAL_VALUE,
  RAILGRID_UI_CORE_VERSION,
  RAILGRID_UI_CORE_VERSION_MARKER,
  RAILGRID_UI_STYLE_ID,
} from './portalkit/styles'

interface FakeStyle {
  id: string
  textContent: string
  attributes: Record<string, string>
  setAttribute(name: string, value: string): void
}

function fakeStyle(id: string): FakeStyle {
  return {
    id,
    textContent: 'stale host stylesheet',
    attributes: {},
    setAttribute(name, value) {
      this.attributes[name] = value
    },
  }
}

function installFakeHost(initialVersion = ''): {
  stale: FakeStyle
  appended: FakeStyle[]
  setVersion(version: string): void
  restore(): void
} {
  const stale = fakeStyle(RAILGRID_UI_STYLE_ID)
  const appended: FakeStyle[] = []
  let version = initialVersion
  const document = {
    documentElement: {},
    getElementById(id: string): FakeStyle | null {
      if (id === stale.id) return stale
      return appended.find(style => style.id === id) ?? null
    },
    createElement(): FakeStyle {
      return fakeStyle('')
    },
    head: {
      appendChild(style: FakeStyle): void {
        appended.push(style)
      },
    },
  }
  const window = {
    getComputedStyle: () => ({
      getPropertyValue(name: string): string {
        if (name === RAILGRID_UI_CANONICAL_MARKER) return RAILGRID_UI_CANONICAL_VALUE
        if (name === RAILGRID_UI_CORE_VERSION_MARKER) return version
        return ''
      },
    }),
  }
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.assign(globalThis, { document, window })

  return {
    stale,
    appended,
    setVersion(nextVersion) {
      version = nextVersion
    },
    restore() {
      if (previousDocument === undefined) delete (globalThis as { document?: unknown }).document
      else globalThis.document = previousDocument
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else globalThis.window = previousWindow
    },
  }
}

describe('PortalKit stylesheet compatibility', () => {
  it('recovers from a stale canonical host without replacing its style node', () => {
    const host = installFakeHost()

    try {
      ensureRailgridUIStyles()

      expect(host.stale.textContent).toBe('stale host stylesheet')
      expect(host.appended).toHaveLength(1)
      expect(host.appended[0].id).toBe(`${RAILGRID_UI_STYLE_ID}-v${RAILGRID_UI_CORE_VERSION}`)
      expect(host.appended[0].attributes['data-railgrid-ui-source']).toBe('portalkit-fallback')
      expect(host.appended[0].attributes['data-railgrid-ui-core-version']).toBe(String(RAILGRID_UI_CORE_VERSION))

      host.setVersion(String(RAILGRID_UI_CORE_VERSION))
      ensureRailgridUIStyles()
      expect(host.appended).toHaveLength(1)
    } finally {
      host.restore()
    }
  })

  it.each([String(RAILGRID_UI_CORE_VERSION), String(RAILGRID_UI_CORE_VERSION + 1)])(
    'accepts a current or newer host stylesheet version (%s)',
    version => {
      const host = installFakeHost(version)

      try {
        ensureRailgridUIStyles()
        expect(host.appended).toHaveLength(0)
      } finally {
        host.restore()
      }
    },
  )
})
