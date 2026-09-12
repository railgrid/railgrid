/** The host owns route focus; providers announce when their destination DOM exists. */
export function createProviderRouteFocus() {
  const sources = new Map<string, HTMLElement>();
  let previousPath = '';
  let previousRoot: HTMLElement | null = null;
  return {
    before(root: HTMLElement, path: string) {
      if (previousRoot !== root) { sources.clear(); previousRoot = root; previousPath = path; return; }
      if (path === previousPath) return;
      const active = document.activeElement;
      if (active instanceof HTMLElement && root.contains(active)) sources.set(previousPath, active);
      previousPath = path;
    },
    ready(root: HTMLElement) {
      if (root !== previousRoot) return;
      // Never take focus away from a control the user reached while rendering.
      if (document.activeElement !== document.body && root.contains(document.activeElement)) return;
      const source = sources.get(previousPath);
      const target = source?.isConnected && root.contains(source) ? source : root.querySelector<HTMLElement>('h1, h2');
      if (!target) return;
      if (!target.hasAttribute('tabindex') && /^H[12]$/.test(target.tagName)) target.tabIndex = -1;
      target.focus({ preventScroll: !!source?.isConnected });
    },
    clear() { sources.clear(); previousRoot = null; previousPath = ''; },
  };
}
