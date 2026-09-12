import { createApp, h, shallowReactive, type App as VueApp } from 'vue';
import App from './App.vue';
import type { FarosContext } from './api';
export class LinearElement extends HTMLElement {
  private app: VueApp | null = null;
  private host: HTMLDivElement | null = null;
  private state = shallowReactive<{ ctx: FarosContext | null }>({ ctx: null });
  set farosContext(value: FarosContext | null) { this.state.ctx = value; }
  get farosContext() { return this.state.ctx; }
  connectedCallback() {
    if (this.app) return;
    this.host = document.createElement('div');
    this.appendChild(this.host);
    this.app = createApp({ render: () => h(App, { ctx: this.state.ctx }) });
    this.app.mount(this.host);
  }
  disconnectedCallback() { this.app?.unmount(); this.app = null; this.host?.remove(); this.host = null; }
}
