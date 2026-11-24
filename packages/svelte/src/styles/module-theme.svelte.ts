// Which module theme this browser draws the app in, remembered between visits.
//
// A module theme is a stylesheet a module's manifest offers under `ui.themes`.
// It is linked after the app's own styles, so it may override anything the app
// draws. The pick is remembered with the stylesheet's address, so a reload
// links it before the listing is read and the page flashes as little as it can
// without the server's help.
import {
  browserStorage,
  readRemembered,
  type RememberedStorage,
  writeRemembered,
} from '../remembered';

/** The one `localStorage` key the pick lives under. */
export const MODULE_THEME_STORAGE_KEY = 'module-theme';

/** The attribute the theme's `<link>` carries, so it is found again. */
const LINK_ATTRIBUTE = 'data-module-theme';

/** One theme a module offers, as the listing names it. */
export interface ModuleThemeOption {
  /** `<namespace>/<id>`, unique on the server. */
  key: string;
  title: string;
  /** The title of the module that offers it. */
  module?: string | undefined;
  /** Where the stylesheet is served. */
  href: string;
}

interface Remembered {
  key: string;
  href: string;
}

const readPick = (
  storage: RememberedStorage | null,
): Remembered | undefined => {
  const raw = readRemembered(storage, MODULE_THEME_STORAGE_KEY);
  if (raw === null) return undefined;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (
      typeof parsed === 'object' &&
      parsed !== null &&
      'key' in parsed &&
      'href' in parsed &&
      typeof parsed.key === 'string' &&
      typeof parsed.href === 'string' &&
      // Only a stylesheet the core serves for a module, never another origin.
      parsed.href.startsWith('/api/v1/modules/')
    ) {
      return { key: parsed.key, href: parsed.href };
    }
  } catch {
    // A value this store did not write is forgotten below.
  }
  return undefined;
};

/** Links the stylesheet, or removes the link for `undefined`. */
const applyStylesheet = (href: string | undefined): void => {
  if (typeof document === 'undefined') return;
  const held = document.head.querySelector<HTMLLinkElement>(
    `link[${LINK_ATTRIBUTE}]`,
  );
  if (href === undefined) {
    held?.remove();
    return;
  }
  const link = held ?? document.createElement('link');
  link.rel = 'stylesheet';
  link.setAttribute(LINK_ATTRIBUTE, '');
  if (link.getAttribute('href') !== href) link.href = href;
  // Last in the head, so it comes after every stylesheet the app linked.
  document.head.append(link);
};

export interface ModuleThemeStoreOptions {
  storage?: RememberedStorage | null;
}

/** A shared store, so a class (svelte-5.md). */
export class ModuleThemeStore {
  readonly #storage: RememberedStorage | null;
  #pick = $state<Remembered | undefined>(undefined);

  constructor({ storage = browserStorage() }: ModuleThemeStoreOptions = {}) {
    this.#storage = storage;
    this.#pick = readPick(storage);
    applyStylesheet(this.#pick?.href);
  }

  /** The key of the theme in use, or `undefined` for the app's own look. */
  get key(): string | undefined {
    return this.#pick?.key;
  }

  /** Draws the app in `option`, or in its own look for `undefined`. */
  choose(option: ModuleThemeOption | undefined): void {
    this.#pick =
      option === undefined ? undefined : { key: option.key, href: option.href };
    applyStylesheet(this.#pick?.href);
    if (this.#pick === undefined) {
      try {
        this.#storage?.removeItem?.(MODULE_THEME_STORAGE_KEY);
      } catch {
        // A browser that refuses to forget still draws the app's own look.
      }
      return;
    }
    writeRemembered(
      this.#storage,
      MODULE_THEME_STORAGE_KEY,
      JSON.stringify(this.#pick),
    );
  }

  /**
   * Holds the pick to what the server offers now. A theme whose module was
   * removed or switched off is dropped, and one whose address moved follows it.
   */
  reconcile(offered: readonly ModuleThemeOption[]): void {
    const pick = this.#pick;
    if (pick === undefined) return;
    const found = offered.find((option) => option.key === pick.key);
    if (found?.href !== pick.href) this.choose(found);
  }
}

export const moduleTheme = new ModuleThemeStore();
