// The app inside PrusaSlicer, and a module page or the app pairing page shown
// embedded (ADR-20261112-2eb rules 4 and 6).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';

import fixtures from '../test/ux/fixtures/api.json';

const bridge = vi.hoisted(() => ({
  installed: false,
  stale: false,
  listeners: [] as (() => void)[],
  watch: vi.fn(),
  refused: vi.fn(),
  onStale(listener: () => void) {
    this.listeners.push(listener);
  },
}));

vi.mock('./lib/slicer/bridge', () => ({
  slicerBridge: () => (bridge.installed ? bridge : undefined),
}));

vi.mock('./lib/screens', async () => {
  const { default: Counting } = await import('../test/counting-screen.svelte');

  return { screenFor: () => Counting };
});

interface Fixture {
  status: number;
  type: string;
  body: string;
}

const serve = (replaced: Record<string, Fixture> = {}): void => {
  const table = { ...(fixtures as Record<string, Fixture>), ...replaced };
  vi.stubGlobal(
    'fetch',
    vi.fn((input: Request | string, init?: RequestInit) => {
      const href = typeof input === 'string' ? input : input.url;
      const method =
        typeof input === 'string' ? (init?.method ?? 'GET') : input.method;
      const row = table[`${method} ${new URL(href, location.href).pathname}`];

      return Promise.resolve(
        new Response(row?.body ?? '{}', {
          status: row?.status ?? 404,
          headers: { 'content-type': row?.type ?? 'application/json' },
        }),
      );
    }),
  );
};

const refused: Fixture = {
  status: 401,
  type: 'application/json',
  body: '{"title":"Unauthorized","status":401}',
};

beforeEach(() => {
  bridge.installed = false;
  bridge.stale = false;
  bridge.listeners = [];
  bridge.watch.mockClear();
  history.replaceState(null, '', '/');
});

afterEach(() => {
  vi.unstubAllGlobals();
  history.replaceState(null, '', '/');
});

describe('the app inside PrusaSlicer', () => {
  it('waits for the slicer instead of offering the sign-in form', async () => {
    bridge.installed = true;
    serve({ 'GET /api/v1/principal': refused });
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect
      .element(
        page.getByRole('heading', {
          level: 1,
          name: 'Waiting for PrusaSlicer',
        }),
      )
      .toBeVisible();
    await expect
      .element(page.getByRole('status'))
      .toHaveAttribute('aria-busy', 'true');
    await expect
      .element(page.getByLabelText('Username'))
      .not.toBeInTheDocument();
    expect(bridge.watch).toHaveBeenCalledOnce();
  });

  it('tells the person to sign in again once the slicer resends a refused token', async () => {
    bridge.installed = true;
    serve({ 'GET /api/v1/principal': refused });
    const { default: App } = await import('./app.svelte');

    await render(App);
    await expect
      .element(page.getByRole('heading', { name: 'Waiting for PrusaSlicer' }))
      .toBeVisible();

    bridge.stale = true;
    for (const listener of bridge.listeners) listener();

    await expect
      .element(
        page.getByRole('heading', {
          level: 1,
          name: 'Sign in again from PrusaSlicer',
        }),
      )
      .toBeVisible();
    await expect.element(page.getByRole('status')).not.toBeInTheDocument();
  });

  it('offers the sign-in form on a page the slicer did not open', async () => {
    serve({ 'GET /api/v1/principal': refused });
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect.element(page.getByLabelText('Username')).toBeVisible();
    await expect
      .element(page.getByText('Waiting for PrusaSlicer'))
      .not.toBeInTheDocument();
  });
});

describe('a module page shown embedded', () => {
  it('draws without the sidebar and the header', async () => {
    serve();
    history.replaceState(null, '', '/m/fleet-stats/stats?embed=1');
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect
      .element(page.getByTestId('counting-screen'))
      .toBeInTheDocument();
    expect(document.querySelector('[data-part="embed-frame"]')).not.toBeNull();
    await expect
      .element(page.getByRole('navigation').first())
      .not.toBeInTheDocument();
  });

  it('keeps the frame on the same page without the parameter', async () => {
    serve();
    history.replaceState(null, '', '/m/fleet-stats/stats');
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect
      .element(page.getByTestId('counting-screen'))
      .toBeInTheDocument();
    await expect.element(page.getByRole('navigation').first()).toBeVisible();
    expect(document.querySelector('[data-part="embed-frame"]')).toBeNull();
  });

  it('still asks the person to sign in', async () => {
    serve({ 'GET /api/v1/principal': refused });
    history.replaceState(null, '', '/m/fleet-stats/stats?embed=1');
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect.element(page.getByLabelText('Username')).toBeVisible();
    await expect
      .element(page.getByTestId('counting-screen'))
      .not.toBeInTheDocument();
  });
});

describe('the app pairing page shown embedded', () => {
  it('draws without the sidebar and the header', async () => {
    serve();
    history.replaceState(null, '', '/pair?code=ABCD-EFGH&embed=1');
    const { default: App } = await import('./app.svelte');

    await render(App);

    await expect
      .element(page.getByTestId('counting-screen'))
      .toBeInTheDocument();
    expect(document.querySelector('[data-part="embed-frame"]')).not.toBeNull();
    await expect
      .element(page.getByRole('navigation').first())
      .not.toBeInTheDocument();
  });
});
