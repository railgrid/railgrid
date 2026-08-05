// Code editor for the Code tab — CodeMirror 6, the editor core behind many
// browser IDEs. Deliberately not Monaco/VS Code-in-a-tab: the provider ships
// a single IIFE bundle with no code-splitting, so Monaco's multi-megabyte
// payload and web workers don't fit. Real VS Code belongs in the sandbox
// (openvscode-server as a template component, proxied through the data
// plane), not in this micro-frontend — see the Code tab's "open in VS Code"
// path when that lands.

import { EditorView, basicSetup } from 'codemirror'
import { EditorState, Compartment } from '@codemirror/state'
import { keymap } from '@codemirror/view'
import { indentWithTab } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { json } from '@codemirror/lang-json'
import { python } from '@codemirror/lang-python'
import { oneDark } from '@codemirror/theme-one-dark'

// languageFor picks a grammar from the file extension. Unknown types get
// plain text — still line-numbered, searchable, and editable.
function languageFor(path: string) {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  switch (ext) {
    case 'js':
    case 'mjs':
    case 'cjs':
      return [javascript()]
    case 'jsx':
      return [javascript({ jsx: true })]
    case 'ts':
      return [javascript({ typescript: true })]
    case 'tsx':
      return [javascript({ typescript: true, jsx: true })]
    case 'html':
    case 'htm':
    case 'vue':
      return [html()]
    case 'css':
    case 'scss':
      return [css()]
    case 'json':
      return [json()]
    case 'py':
      return [python()]
    default:
      return []
  }
}

export interface EditorHandle {
  dom: HTMLElement
  path: string
  setDoc(path: string, content: string): void
  doc(): string
  dirty(): boolean
  markSaved(): void
  focused(): boolean
  destroy(): void
}

// createEditor builds a detached editor. The host moves its DOM node into
// place on every render (appendChild moves rather than clones, so editor
// state — cursor, scroll, history — survives the surrounding re-render).
export function createEditor(opts: {
  path: string
  content: string
  dark: boolean
  onChange: () => void
  onSave: () => void
}): EditorHandle {
  const language = new Compartment()
  let saved = opts.content

  const view = new EditorView({
    state: EditorState.create({
      doc: opts.content,
      extensions: [
        basicSetup,
        keymap.of([
          indentWithTab,
          {
            key: 'Mod-s',
            preventDefault: true,
            run: () => {
              opts.onSave()
              return true
            },
          },
        ]),
        language.of(languageFor(opts.path)),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) opts.onChange()
        }),
        ...(opts.dark ? [oneDark] : []),
        EditorView.theme({
          '&': { height: '100%', fontSize: '12.5px' },
          '.cm-scroller': { fontFamily: 'var(--vibe-mono, monospace)' },
        }),
      ],
    }),
  })

  const handle: EditorHandle = {
    dom: view.dom,
    path: opts.path,
    setDoc(path, content) {
      handle.path = path
      saved = content
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: content },
        effects: language.reconfigure(languageFor(path)),
      })
    },
    doc: () => view.state.doc.toString(),
    dirty: () => view.state.doc.toString() !== saved,
    markSaved() {
      saved = view.state.doc.toString()
    },
    focused: () => view.hasFocus,
    destroy: () => view.destroy(),
  }
  return handle
}
