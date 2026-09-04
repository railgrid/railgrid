import { describe, expect, it } from 'vitest'
import {
  ensureFarosUIStyles,
  FAROS_UI_CANONICAL_MARKER,
  FAROS_UI_CANONICAL_VALUE,
  FAROS_UI_STYLE_ID,
  FAROS_UI_VERSION,
  FAROS_UI_VERSION_MARKER,
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
  const stale = fakeStyle(FAROS_UI_STYLE_ID)
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
        if (name === FAROS_UI_CANONICAL_MARKER) return FAROS_UI_CANONICAL_VALUE
        if (name === FAROS_UI_VERSION_MARKER) return version
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
      ensureFarosUIStyles()

      expect(host.stale.textContent).toBe('stale host stylesheet')
      expect(host.appended).toHaveLength(1)
      expect(host.appended[0].id).toBe(`${FAROS_UI_STYLE_ID}-v${FAROS_UI_VERSION}`)
      expect(host.appended[0].attributes['data-faros-ui-source']).toBe('portalkit-fallback')
      expect(host.appended[0].attributes['data-faros-ui-version']).toBe(String(FAROS_UI_VERSION))

      host.setVersion(String(FAROS_UI_VERSION))
      ensureFarosUIStyles()
      expect(host.appended).toHaveLength(1)
    } finally {
      host.restore()
    }
  })

  it.each([String(FAROS_UI_VERSION), String(FAROS_UI_VERSION + 1)])(
    'accepts a current or newer host stylesheet version (%s)',
    version => {
      const host = installFakeHost(version)

      try {
        ensureFarosUIStyles()
        expect(host.appended).toHaveLength(0)
      } finally {
        host.restore()
      }
    },
  )
})
