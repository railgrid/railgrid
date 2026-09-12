import { expect, it } from 'vitest';
import { createProviderRouteFocus } from '../../../../portal/src/providers/providerRouteFocus';
it('the host focuses route headings, restores cached source controls and fences remounts', () => {
  const focus = createProviderRouteFocus(); const root = document.createElement('div'); document.body.append(root);
  root.innerHTML = '<button>Issue</button>'; const button = root.querySelector('button')!;
  focus.before(root, 'issues'); button.focus(); focus.before(root, 'detail'); button.remove();
  root.innerHTML = '<h1>Issue detail</h1>'; focus.ready(root); expect(document.activeElement).toBe(root.querySelector('h1'));
  focus.before(root, 'issues'); root.replaceChildren(button); focus.ready(root); expect(document.activeElement).toBe(button);
  focus.clear(); root.remove();
});
