import { LinearElement } from './element';
import { ensureFarosUIStyles } from './portalkit/styles';
import styles from './style.css?inline';
ensureFarosUIStyles();
if (!customElements.get('faros-provider-linear')) {
  const style = document.createElement('style');
  style.id = 'faros-provider-linear-css';
  style.textContent = styles;
  document.head.appendChild(style);
  customElements.define('faros-provider-linear', LinearElement);
}
