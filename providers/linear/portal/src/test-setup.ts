import { vi } from 'vitest';
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
Object.defineProperty(window, 'matchMedia', { value: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) });
Element.prototype.scrollIntoView = () => {};
