import { afterEach, describe, expect, it } from 'vitest';

import type { RememberedStorage } from '../remembered';
import {
  MODULE_THEME_STORAGE_KEY,
  type ModuleThemeOption,
  ModuleThemeStore,
} from './module-theme.svelte';

const fakeStorage = (seed?: string) => {
  const map = new Map<string, string>();
  if (seed !== undefined) map.set(MODULE_THEME_STORAGE_KEY, seed);
  const storage: RememberedStorage = {
    getItem: (key) => map.get(key) ?? null,
    setItem: (key, value) => {
      map.set(key, value);
    },
    removeItem: (key) => {
      map.delete(key);
    },
  };
  return { map, storage };
};

const OCEAN: ModuleThemeOption = {
  key: 'com.github.aux.acme.themes/ocean',
  title: 'Ocean',
  module: 'Acme themes',
  href: '/api/v1/modules/com.github.aux.acme.themes/ui/ocean.css',
};

const linked = (): string | null | undefined =>
  document.querySelector('link[data-module-theme]')?.getAttribute('href');

afterEach(() => {
  document.querySelector('link[data-module-theme]')?.remove();
});

describe('ModuleThemeStore', () => {
  it('starts on the app’s own look with nothing stored', () => {
    const { storage } = fakeStorage();
    const store = new ModuleThemeStore({ storage });

    expect(store.key).toBeUndefined();
  });

  it('remembers a pick with its stylesheet, and forgets it on None', () => {
    const { map, storage } = fakeStorage();
    const store = new ModuleThemeStore({ storage });

    store.choose(OCEAN);
    expect(store.key).toBe(OCEAN.key);
    expect(JSON.parse(map.get(MODULE_THEME_STORAGE_KEY) ?? '{}')).toEqual({
      key: OCEAN.key,
      href: OCEAN.href,
    });

    store.choose(undefined);
    expect(store.key).toBeUndefined();
    expect(map.has(MODULE_THEME_STORAGE_KEY)).toBe(false);
  });

  it('reads back a remembered pick before the listing answers', () => {
    const { storage } = fakeStorage(
      JSON.stringify({ key: OCEAN.key, href: OCEAN.href }),
    );

    expect(new ModuleThemeStore({ storage }).key).toBe(OCEAN.key);
  });

  it('refuses a remembered stylesheet from outside the module prefix', () => {
    const { storage } = fakeStorage(
      JSON.stringify({ key: OCEAN.key, href: 'https://example.com/x.css' }),
    );

    expect(new ModuleThemeStore({ storage }).key).toBeUndefined();
  });

  it('ignores a stored value it did not write', () => {
    const { storage } = fakeStorage('not json');

    expect(new ModuleThemeStore({ storage }).key).toBeUndefined();
  });

  it('drops a pick the server no longer offers', () => {
    const { map, storage } = fakeStorage();
    const store = new ModuleThemeStore({ storage });
    store.choose(OCEAN);

    store.reconcile([]);

    expect(store.key).toBeUndefined();
    expect(map.has(MODULE_THEME_STORAGE_KEY)).toBe(false);
  });

  it('follows a stylesheet whose address moved', () => {
    const { storage } = fakeStorage();
    const store = new ModuleThemeStore({ storage });
    store.choose(OCEAN);
    const moved = { ...OCEAN, href: `${OCEAN.href}?v=2` };

    store.reconcile([moved]);

    expect(store.key).toBe(OCEAN.key);
    expect(linked()).toBe(moved.href);
  });
});
